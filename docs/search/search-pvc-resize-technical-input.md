# Technical Input: Manually Expand MongoDB Search Index Storage

> **Purpose:** Provide a Search-specific procedure that preserves existing index claims while you expand their storage.
> **Source PR:** [mongodb/mongodb-kubernetes#1621](https://github.com/mongodb/mongodb-kubernetes/pull/1621).
> **Documentation deliverable:** Create a separate Search tutorial. This document is technical input, not a published procedure or an executable snippet suite.

## Summary

Document manual expansion of the persistent volumes that store mongot indexes. Stop the responsible Kubernetes Operator deployments before you orphan-delete the mongot StatefulSets. Update the existing MongoDBSearch resource, then explicitly resume those operators at their original replica counts.

This procedure introduces no API, status field, or native expansion feature. The separate page is an editorial recommendation, not a technical requirement. A clearly separated Search section in the existing storage page can describe the same procedure.

## Current Documentation and Requested Placement

Create **Manually Expand MongoDB Search Index Storage**, with the recommended path `tutorial/resize-search-pv-storage`. Link to the new page from the storage overview and Search deployment guides.

Cross-link [Increase Storage for Persistent Volumes](https://www.mongodb.com/docs/kubernetes/current/tutorial/resize-pv-storage/). Retain that page's distinction between manual and native expansion for other resource types. Do not add Search to the native expansion workflow.

The existing manual procedure omits operator resumption. It also requires `FileSystemResizePending`, which online expansion can skip. Do not copy these issues. PVCs and PVs use `Bound`, not `Running`. Check StatefulSet readiness through replica counts and pod readiness, not a StatefulSet `Running` phase.

## Scope and Prerequisites

The procedure targets existing, filesystem-backed mongot `data` claims. Expand storage only. Keep the StorageClass, access modes, volume mode, claim names, and bindings unchanged.

Before you start:

- Confirm that the source database and Search deployment are healthy. Save a representative query and its expected results.
- Identify every affected mongot StatefulSet, pod, claim, member context, Search resource, and responsible operator deployment. Do not select claims by name substrings.
- Confirm that the StorageClass has `allowVolumeExpansion: true`. This flag does not add expansion support to the CSI driver or storage backend.
- Confirm that your CSI driver supports controller expansion while the volume remains attached. Confirm its filesystem expansion requirements and supported filesystem types.
- Establish a backup or recovery plan for the source data and Search configuration. Account for the time and capacity needed to rebuild indexes if recovery requires it.
- Arrange a maintenance window. Pause GitOps reconciliation, autoscaling, upgrades, and other changes to the affected Search resources and operators.
- Review all resources managed by each operator that you stop. Operator downtime pauses their reconciliation, not just this Search deployment.
- Obtain read access to StorageClasses, PVs, PVCs, pods, StatefulSets, Deployments, and MongoDBSearch resources in the relevant contexts. Obtain PVC patch, StatefulSet delete, Deployment scale, Search patch, and conditional pod delete permissions.
- Install `kubectl` and `jq`. Use a Kubernetes quantity parser, such as `kubernetes.utils.parse_quantity`, when comparing capacities. Quantities such as `2Gi` and `2048Mi` are equivalent.

Exclude drivers that require backend/controller expansion with the volume detached. The sequence keeps mongot pods mounted while the controller expands storage. Do not adapt it by scaling mongot to zero. Such backends need a separately designed and validated maintenance procedure.

### Protect Claims Independently of the Operator

MongoDB Search StatefulSets use `whenDeleted: Delete` and `whenScaled: Delete` retention policies beginning with Kubernetes Operator 1.9. Inspect the live policy rather than assuming defaults.

Deleting a MongoDBSearch resource can invoke the Search controller's deletion cleanup. Deleting a StatefulSet or reducing its replicas can separately trigger Kubernetes PVC retention and garbage collection. **Stopping the operator does not stop Kubernetes garbage collection.**

Keep the MongoDBSearch resource intact. Delete only the inventoried StatefulSet object, with `--cascade=orphan`. Keep mongot replica counts unchanged. Never delete PVCs, remove finalizers, or edit `PV.spec.capacity`.

The PV reclaim policy and StatefulSet PVC retention policy govern different lifecycles. A PV reclaim policy of `Retain` does not prevent Kubernetes from deleting the PVC. A retained PV alone does not prove that mongot still uses its original claim.

## Topology and Effective Storage Inventory

Use the API shape on the source PR. In this revision, the stable cluster identifier is `spec.clusters[].index`, not `clusterIndex`. The array offset is not the index.

| Mode | MongoDBSearch location | mongot StatefulSet and PVC location | Operator deployments to stop and resume |
| --- | --- | --- | --- |
| Single cluster | Workload cluster | Same cluster | Deployment that reconciles this Search resource |
| Multi-cluster, central operator | Central cluster | Each affected member cluster | Central operator deployment, including all its replicas |
| Simulated multi-cluster | Local copy in each member cluster | Local member cluster | Each affected member's Search operator deployment |

In simulated multi-cluster mode, each operator projects the local entry from a Search resource that lists all clusters. Keep the intended configuration consistent across all local copies. Do not stop a separate source-database operator unless it also reconciles the affected Search resources.

For replica-set sources, resource names include the explicit index: `<search>-search-<index>`. For sharded sources, they also include the shard name: `<search>-search-<index>-<shardName>`. Confirm names against the live resources and their labels. Never derive the index from the array offset.

Resolve storage for every target:

1. Read `spec.clusters[<offset>].persistence.single.storage`.
2. If a matching `spec.clusters[<offset>].shardOverrides[]` entry sets `persistence`, that whole object replaces cluster persistence for those shards.
3. Inspect `statefulSet.spec.volumeClaimTemplates` at the cluster and matching shard-override levels. A custom named claim template can override the generated persistence size.
4. Compare the effective size with the live template named `data` and the exact claims mounted by the pods.

The commands below cover replica-set sources with an existing `persistence.single.storage` field and no custom claim templates. Do not apply their patch path to shard overrides or custom templates. Inventory those configurations separately and update the field that actually controls each affected template. Sharded overrides and custom templates remain outside this PR's resize validation.

**Never use a partial JSON merge patch containing a shortened `spec.clusters` array.** JSON merge patch replaces the entire array. Removing an entry can invoke removed-cluster cleanup and delete Search resources and their claims.

## Ordered Procedure

Apply each phase to **all affected units before advancing to the next phase**. A unit comprises a member context, namespace, StatefulSet, and its exact mounted claims. Record each operator once, even if it manages several units.

The variables below represent one unit. Repeat the inventory for each affected unit and keep separate records. Use the actual deployment and namespace from your installation.

```sh
MEMBER_CONTEXT=member-context
CR_CONTEXT=central-or-local-context
OP_CONTEXT=operator-context
NS=search-namespace
OP_NS=operator-namespace
SEARCH=my-search
STS=my-search-search-1
OPERATOR=mongodb-kubernetes-operator
NEW=2Gi
```

### 1. Record the Ready Baseline

Save the full Search resource, StatefulSet, operator deployment, pods, and claims. Store records in a separate directory for each context and StatefulSet. Protect any exported configuration according to your organization's policy.

```sh
kubectl --context "$CR_CONTEXT" -n "$NS" get mongodbsearch "$SEARCH" \
  -o json > search-before.json
kubectl --context "$MEMBER_CONTEXT" -n "$NS" get sts "$STS" \
  -o json > sts-before.json
kubectl --context "$OP_CONTEXT" -n "$OP_NS" get deployment "$OPERATOR" \
  -o json > operator-before.json
ORIGINAL_REPLICAS=$(jq -er '.spec.replicas' operator-before.json)
STS_UID=$(jq -er '.metadata.uid' sts-before.json)
OP_SELECTOR=$(jq -er \
  '.spec.selector.matchLabels | to_entries | map("\(.key)=\(.value)") | join(",")' \
  operator-before.json)

kubectl --context "$MEMBER_CONTEXT" -n "$NS" get pods -o json |
  jq --arg uid "$STS_UID" \
    '[.items[] | select(any(.metadata.ownerReferences[]?; .uid == $uid))]' \
  > pods-before.json
jq -r '.[].spec.volumes[] |
  select(.name == "data") | .persistentVolumeClaim.claimName' \
  pods-before.json | sort -u > claims.txt
test -s claims.txt
while IFS= read -r pvc; do
  kubectl --context "$MEMBER_CONTEXT" -n "$NS" get pvc "$pvc" -o json \
    > "$pvc-before.json"
done < claims.txt
```

Check that the operator selector identifies only this deployment's pods. The selector example assumes `matchLabels` without `matchExpressions`. Review other selector shapes separately.

Require all expected mongot pods to be Ready, with the recorded StatefulSet owner UID and no deletion timestamp. Verify that every pod mounts exactly one inventoried `data` claim. Record each claim's UID, PV name, requested size, capacity, owner references, and `Bound` phase. Confirm that the original operator replica count is greater than zero.

Record the query baseline before expanding anything. The e2e additionally writes a per-pod marker file into the data volume. This marker is test evidence, not a requirement to modify a production index directory.

### 2. Expand the Exact Claims

Confirm that `NEW` exceeds each current request. Patch only names recorded from the target pods:

```sh
while IFS= read -r pvc; do
  kubectl --context "$MEMBER_CONTEXT" -n "$NS" patch pvc "$pvc" \
    --type=merge \
    -p "$(jq -nc --arg size "$NEW" \
      '{spec:{resources:{requests:{storage:$size}}}}')"
done < claims.txt
```

Allow up to 15 minutes for controller expansion, or use the documented bound for your storage backend. Inspect each exact claim and its recorded PV:

```sh
kubectl --context "$MEMBER_CONTEXT" -n "$NS" get pvc "$PVC" -o yaml
kubectl --context "$MEMBER_CONTEXT" get pv "$PV" -o yaml
```

Set `PVC` and `PV` from that unit's inventory before these commands. Require the original claim UID and PV binding, `Bound`, no deletion timestamp, and a request greater than or equal to `NEW`.

Proceed if the PV reports at least `NEW` and either of these conditions holds:

- The PVC capacity reports at least `NEW`. The CSI driver can finish filesystem expansion online without a `FileSystemResizePending` condition.
- The PVC reports `FileSystemResizePending=True` after controller expansion. Defer the conditional pod restart until the operator has recreated and adopted the StatefulSet.

Compare quantities numerically, not as strings. If neither condition holds before the timeout, stop and investigate CSI events and logs. Do not fabricate completion by editing the PV capacity.

### 3. Stop the Responsible Operators

Scale only the inventoried operator deployments to zero:

```sh
kubectl --context "$OP_CONTEXT" -n "$OP_NS" scale deployment "$OPERATOR" \
  --current-replicas="$ORIGINAL_REPLICAS" --replicas=0
kubectl --context "$OP_CONTEXT" -n "$OP_NS" wait --for=delete pod \
  -l "$OP_SELECTOR" --timeout=300s
kubectl --context "$OP_CONTEXT" -n "$OP_NS" get deployment "$OPERATOR" \
  -o jsonpath='{.spec.replicas}{"\n"}'
kubectl --context "$OP_CONTEXT" -n "$OP_NS" get pods -l "$OP_SELECTOR"
```

Require a desired replica count of zero and **no remaining operator pods**, including terminating pods. If the wait command reports no matching resources, confirm the empty list explicitly. Stop all responsible deployments before orphaning any target.

In central multi-cluster mode, use the central operator context. In simulated multi-cluster mode, repeat this phase for every affected member's local Search operator. Stopping only the central source operator does not pause the member Search operators.

Prepare the resume commands in phase 6 now. If a later phase fails, follow the recovery instructions below rather than leaving the operators stopped indefinitely.

### 4. Orphan-Delete Only the Target StatefulSets

```sh
kubectl --context "$MEMBER_CONTEXT" -n "$NS" delete sts "$STS" \
  --cascade=orphan --wait=true --timeout=300s
kubectl --context "$MEMBER_CONTEXT" -n "$NS" get sts "$STS"
```

Require `NotFound` for each targeted StatefulSet while its operator remains stopped. Confirm that it stays absent after the CR update in phase 5.

Read every recorded pod and claim by exact name. Require unchanged pod and PVC UIDs, unchanged PV bindings, and no deletion timestamps. Confirm the mongot replicas survive. Never substitute a plain StatefulSet delete, delete the Search resource, or scale mongot down.

### 5. Update the Existing Search Resources

Read the CR again after the operators stop. Locate the array offset by the entry's explicit index and name, not by treating the index as an offset:

```sh
kubectl --context "$CR_CONTEXT" -n "$NS" get mongodbsearch "$SEARCH" \
  -o json > search-paused.json
jq '.spec.clusters | to_entries |
  map({offset:.key, name:.value.name, index:.value.index,
       persistence:.value.persistence, shardOverrides:.value.shardOverrides,
       statefulSet:.value.statefulSet})' search-paused.json
```

For the supported example, set `OFFSET` to the inspected array offset. The following patch checks the resource version and the entire selected entry before changing only its storage field:

```sh
OFFSET=0
jq --argjson offset "$OFFSET" --arg size "$NEW" '
  ("/spec/clusters/" + ($offset | tostring)) as $path |
  [
    {op:"test", path:"/metadata/resourceVersion", value:.metadata.resourceVersion},
    {op:"test", path:$path, value:.spec.clusters[$offset]},
    {op:"replace", path:($path + "/persistence/single/storage"), value:$size}
  ]' search-paused.json > resize-patch.json
kubectl --context "$CR_CONTEXT" -n "$NS" patch mongodbsearch "$SEARCH" \
  --type=json --patch-file=resize-patch.json
kubectl --context "$CR_CONTEXT" -n "$NS" get mongodbsearch "$SEARCH" -o json
```

Verify the same CR UID, all original cluster entries and indexes, unchanged replicas, and the new effective storage value. If you update another entry, fetch the current resource version and regenerate the patch first. A failed `test` operation requires investigation, not removal of the guard.

For central multi-cluster mode, patch the central CR's affected entries. For simulated multi-cluster mode, update the corresponding entries in **every local CR copy**. Preserve all other entries and settings.

Require every targeted StatefulSet to remain absent and every retained pod and claim to remain unchanged. A stale `Running` status while the operator is stopped does not demonstrate reconciliation. Do not wait for or require an immutable-template `Failed` state.

### 6. Resume Operators and Verify Adoption

Restore each operator's own recorded replica count:

```sh
kubectl --context "$OP_CONTEXT" -n "$OP_NS" scale deployment "$OPERATOR" \
  --current-replicas=0 --replicas="$ORIGINAL_REPLICAS"
kubectl --context "$OP_CONTEXT" -n "$OP_NS" rollout status \
  deployment/"$OPERATOR" --timeout=600s
```

Verify desired, updated, and ready replicas equal that original count. Do not unconditionally restore one replica.

Allow up to 10 minutes for each affected StatefulSet to reappear. Before checking global Search readiness, verify **each unit**:

- The StatefulSet has a new UID, its original replica count, and the requested storage in the claim template named `data`.
- Existing pods retain their UIDs, and their controller owner references point to the new StatefulSet.
- PVCs retain their UIDs and PV bindings, remain `Bound`, and have no deletion timestamps.
- If the live retention policy uses `whenDeleted: Delete`, PVC owner references point to the new StatefulSet UID.

Compare container restart-count deltas with the baseline if you check for unexpected restarts. An existing pod can legitimately have a nonzero lifetime restart count.

### 7. Complete Filesystem Expansion If Required

If `FileSystemResizePending=True` persists after allowing time for online completion, restart the affected mongot pods **one at a time**. First confirm that the recreated StatefulSet owns the pod and that all other expected pods are Ready.

```sh
POD=exact-pod-name-from-inventory
OLD_POD_UID=$(kubectl --context "$MEMBER_CONTEXT" -n "$NS" get pod "$POD" \
  -o jsonpath='{.metadata.uid}')
kubectl --context "$MEMBER_CONTEXT" -n "$NS" delete pod "$POD" \
  --wait=true --timeout=300s
kubectl --context "$MEMBER_CONTEXT" -n "$NS" wait --for=condition=Ready \
  pod/"$POD" --timeout=600s
kubectl --context "$MEMBER_CONTEXT" -n "$NS" get pod "$POD" \
  -o jsonpath='{.metadata.uid}{"\n"}'
```

If the replacement does not exist when `wait` starts, retry the readiness command within the same 10-minute bound. Require a UID different from `OLD_POD_UID`. Verify that the replacement mounts the same claim and that the claim reports capacity at least `NEW`, without a pending filesystem resize.

Check the representative Search query before restarting the next pod. Direct pod deletion bypasses PodDisruptionBudgets. A single mongot replica cannot serve requests while it restarts. This procedure does not guarantee zero downtime, even with multiple replicas.

Skip production pod deletion if expansion completes online. The e2e performs an additional serial restart even in that case, solely to test persistence across the next rollout.

### 8. Confirm Completion

Require every affected StatefulSet and pod to be ready. Require each MongoDBSearch CR to report `Running` for its current generation after all units reconcile. Check each member's local Search path, not just the source replica set's current primary.

Verify the original PVC UIDs and PV bindings, numeric expanded requests and capacity, no deletion timestamps, and the expected pod and PVC ownership. Confirm the baseline query results. Insert a deterministic new source document and confirm that each member's Search path indexes it.

On a real storage backend, also verify usable filesystem capacity using that backend's supported tools. A PVC status update does not independently prove usable filesystem growth. In particular, CSI hostpath exposes the node filesystem, so `df` does not measure the claim's requested capacity.

Stable query results after retries do not prove uninterrupted service or absence of re-ingestion. Claim identity, marker survival, query recovery, and continued ingestion establish narrower, distinct facts.

## Failure and Resume Instructions

If expansion fails before you stop the operators, leave them running and inspect CSI events. Do not reduce the requested size to undo an expansion.

If an operation fails while operators are stopped, record which StatefulSets are absent and which CR updates persisted. Keep the Search resources, pods, and claims intact. Complete or correct the effective storage updates where possible, then restore every paused operator to its recorded replica count.

If you must resume before correcting a CR, its recreated StatefulSet can retain the old template size even though the claim grew. Treat that as incomplete. Plan another controlled stop/orphan/update/resume cycle rather than shrinking the claim or deleting it.

If any retained claim or pod has a deletion timestamp, stop further deletes immediately and involve the Kubernetes/storage administrator. Restoring an operator does not cancel Kubernetes garbage collection. Do not remove finalizers as a recovery shortcut.

The e2e registers restoration before scaling each deployment and attempts every restoration even if another cleanup fails. Production operators need equivalent operational ownership and a documented resume plan.

## Validation Matrix

Fresh validation is pending for the stop-and-resume implementation. Replace the pending cells with the exact tested commit, Evergreen task links, and terminal results before treating this document as validated.

| Mode | Resize task | Evergreen variant | Tested commit and result |
| --- | --- | --- | --- |
| Single cluster, Community replica-set source, one mongot | `e2e_search_pvc_resize_workaround` | `e2e_mdb_kind_ubi_cloudqa_large` | Pending |
| Central operator, external replica-set source, two members, one mongot per member | `e2e_search_pvc_resize_workaround_mc` | `e2e_multi_cluster_2_clusters` | Pending |
| Simulated multi-cluster, external replica-set source, two members, three mongots per member | `e2e_search_pvc_resize_workaround_simulated_mc` | `e2e_multi_cluster_kind` | Pending |

All resize cases use `csi-hostpath-sc` and grow claims from `1Gi` to `2Gi`. Multi-cluster cases reverse the CR array while preserving explicit indexes, including a nonzero index. They exercise the complete resize sequence in every targeted member.

The historical single-cluster run on revision `1c68c05bf` does **not** validate this stop-and-resume flow. Remaining validation gaps include sharded sources, shard persistence overrides, custom claim templates, other CSI drivers, and usable filesystem growth on a capacity-enforcing backend. Backend-offline expansion is excluded, not implicitly supported. The filesystem-pending branch only gains runtime evidence if the tested driver actually enters that condition.

## API and Documentation Changes

No settings, status fields, or defaults change. The examples update the existing `spec.clusters[].persistence.single.storage` field. Retain the installed resource's complete source, security, load-balancer, and cluster configuration.

## Code References and Sources

- [Search sizing and API shapes](../../api/mongodb/v1/search/mongodbsearch_types.go): `ClusterSpec`, `ShardOverride`, `ResolveSizingForClusterShard`.
- [Mongot construction and custom StatefulSet overrides](../../controllers/searchcontroller/search_construction.go): named data claim, retention policy, and final override application.
- [Search CR deletion callback](../../controllers/operator/mongodbsearch_controller.go): `OnDelete`.
- [Search reconciliation and removed-cluster cleanup](../../controllers/searchcontroller/mongodbsearch_reconcile_helper.go).
- [Shared resize lifecycle](../../docker/mongodb-kubernetes-tests/tests/common/search/search_deployment_helper.py): `SearchPVCResize`, `run_search_pvc_resize`.
- [Single-cluster test](../../docker/mongodb-kubernetes-tests/tests/search/search_pvc_resize_workaround.py).
- [Central multi-cluster test](../../docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py).
- [Simulated multi-cluster test](../../docker/mongodb-kubernetes-tests/tests/multicluster_search/simulated_mc_rs.py).
- [Existing storage page, Markdown source](https://www.mongodb.com/docs/kubernetes/current/tutorial/resize-pv-storage.md).
- [Kubernetes persistent volume expansion](https://kubernetes.io/docs/concepts/storage/persistent-volumes/#expanding-persistent-volumes-claims).
- [Kubernetes StatefulSet PVC retention](https://kubernetes.io/docs/concepts/workloads/controllers/statefulset/#persistentvolumeclaim-retention).
- [Kubernetes disruptions and direct deletion](https://kubernetes.io/docs/concepts/workloads/pods/disruptions/).

## Notes for the Documentation Writer

Create the requested separate Search page and link it from the storage overview. Placement can change editorially, but preserve the Search-specific safety requirements and the explicit resume step.

Use the source PR's API shape and confirm release applicability before publication. The shell examples illustrate bounded, reviewed operations for the stated supported case. Evergreen runs the equivalent Python-client sequence, not these shell blocks.

Do not imply native expansion, universal CSI support, zero downtime, or guaranteed absence of re-ingestion. Keep the central and simulated multi-cluster instructions distinct. Do not publish a generic array-replacement patch or a command that selects claims by a name prefix.
