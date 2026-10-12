# Manually Expand MongoDB Search Index Storage

You can increase the storage available to MongoDB Search indexes by expanding the persistent volume claims (PVCs) mounted by mongot. This procedure pauses the Kubernetes Operator while you replace the StatefulSet definition and retain its existing pods and claims.

For other MongoDB resources, see [Increase Storage for Persistent Volumes](https://www.mongodb.com/docs/kubernetes/current/tutorial/resize-pv-storage/).

## Prerequisites

Before you begin:

- Confirm that the source database and Search deployment are healthy. Save a representative Search query and its expected results.
- Confirm that the StorageClass has `allowVolumeExpansion: true` and that the CSI driver and filesystem support expansion. Enabling the StorageClass flag alone does not add driver support.
- Confirm that the storage backend can expand the volume while it remains attached. This procedure does not cover backends that require detaching the volume for controller expansion.
- Prepare a backup or recovery plan for the source data and Search configuration. Allow for index rebuild time if recovery requires it.
- Schedule a maintenance window. Pause GitOps reconciliation, scaling, and upgrades for the affected resources. Stopping an operator pauses reconciliation for **all** resources it manages.
- Obtain permission to read the relevant storage and workload resources, patch PVCs, edit MongoDBSearch resources, scale operator Deployments, and delete StatefulSets and pods.
- Install `kubectl` and `jq`, and configure access to every affected Kubernetes context.

> **Important:** You can increase storage, but you cannot decrease it. Keep the StorageClass, access modes, volume mode, claim names, and PV bindings unchanged.

### Identify the Affected Resources

Identify each mongot StatefulSet and the operator that manages it:

| Deployment | MongoDBSearch resource | mongot StatefulSets and PVCs | Operator to stop and resume |
| --- | --- | --- | --- |
| Single cluster | Workload cluster | Same cluster | Operator in that cluster |
| Multi-cluster with a central operator | Central cluster | Each affected member cluster | Central operator |
| Multi-cluster with an operator per member | Local copy in each member | Local member cluster | Each affected member's Search operator |

With an operator per member, keep the intended settings consistent across all local Search resource copies. Do not stop a separate source-database operator unless it also manages the affected Search resources.

Match each StatefulSet to the cluster entry's `name` and explicit `index`. The index is not the entry's position in `spec.clusters`. Replica-set sources use `<search>-search-<index>` names. Sharded sources also include the shard name.

Check which field controls each StatefulSet's storage:

- `spec.clusters[].persistence.single.storage` sets the cluster's index storage.
- A matching `spec.clusters[].shardOverrides[].persistence` replaces the entire cluster persistence configuration for those shards.
- A custom `statefulSet.spec.volumeClaimTemplates` at the cluster or shard-override level can override the generated claim template.

The examples below use `persistence.single.storage` without shard overrides or custom claim templates. If you use overrides, identify the effective field for every affected StatefulSet before proceeding.

> **Important:** Search StatefulSets use `whenDeleted: Delete` and `whenScaled: Delete` PVC retention policies beginning with Kubernetes Operator 1.9. Inspect the live policy. Stopping the operator does **not** stop Kubernetes garbage collection. Keep the MongoDBSearch resource and PVCs intact, keep mongot replica counts unchanged, and delete StatefulSets only with `--cascade=orphan`.
>
> Deleting the Search resource can trigger operator cleanup. Deleting or scaling a StatefulSet can separately trigger PVC deletion. A PV reclaim policy of `Retain` does not prevent deletion of its PVC.

## Manually Expand Storage

Complete each step for all affected StatefulSets before proceeding to the next step. Record each responsible operator once. Use separate records for each Kubernetes context and StatefulSet.

Set the following variables to match your deployment. In a single-cluster deployment, the contexts can have the same value:

```sh
MEMBER_CONTEXT="<member-context>"
CR_CONTEXT="<search-resource-context>"
OP_CONTEXT="<operator-context>"
NS="<search-namespace>"
OP_NS="<operator-namespace>"
SEARCH="<mongodbsearch-name>"
STS="<mongot-statefulset-name>"
OPERATOR="<operator-deployment-name>"
```

1. **Record the existing resources.**

   Save the Search resource, StatefulSet, and operator deployment:

   ```sh
   kubectl --context "$CR_CONTEXT" -n "$NS" get mongodbsearch "$SEARCH" \
     -o yaml > search-before.yaml
   kubectl --context "$MEMBER_CONTEXT" -n "$NS" get sts "$STS" \
     -o json > sts-before.json
   kubectl --context "$OP_CONTEXT" -n "$OP_NS" get deployment "$OPERATOR" \
     -o json > operator-before.json
   ORIGINAL_REPLICAS=$(jq -er '.spec.replicas' operator-before.json)
   STS_UID=$(jq -er '.metadata.uid' sts-before.json)
   ```

   Find the pods owned by this StatefulSet and the exact `data` claims they mount:

   ```sh
   kubectl --context "$MEMBER_CONTEXT" -n "$NS" get pods -o json |
     jq --arg uid "$STS_UID" \
       '[.items[] | select(any(.metadata.ownerReferences[]?; .uid == $uid))]' \
     > pods-before.json
   jq -r '.[].spec.volumes[] | select(.name == "data") |
     .persistentVolumeClaim.claimName' pods-before.json | sort -u > claims.txt
   ```

   Confirm that every expected mongot pod is Ready and appears in the saved list. Verify that each pod mounts one recorded data claim. Do not select claims using a name prefix or substring.

   For every claim, record its UID, PV name, request, capacity, owner references, and `Bound` phase. Confirm that pods and claims have no deletion timestamps. Confirm that the original operator replica count is greater than zero.

   ```sh
   test -s claims.txt
   while IFS= read -r pvc; do
     kubectl --context "$MEMBER_CONTEXT" -n "$NS" get pvc "$pvc" \
       -o yaml > "$pvc-before.yaml"
   done < claims.txt
   ```

2. **Expand each recorded claim.**

   Choose a size greater than each current request. This example expands the claims to `2Gi`:

   ```sh
   while IFS= read -r pvc; do
     kubectl --context "$MEMBER_CONTEXT" -n "$NS" patch pvc "$pvc" \
       --type=merge \
       -p '{"spec":{"resources":{"requests":{"storage":"2Gi"}}}}'
   done < claims.txt
   ```

   Inspect each claim and its recorded PV. Replace the placeholders with exact names:

   ```sh
   kubectl --context "$MEMBER_CONTEXT" -n "$NS" get pvc "<claim-name>" -o yaml
   kubectl --context "$MEMBER_CONTEXT" get pv "<volume-name>" -o yaml
   ```

   Wait up to 15 minutes, or the documented timeout for your backend, for controller expansion. Require the PV capacity to reach the requested size. The PVC must either report the expanded capacity or report `FileSystemResizePending=True` after controller expansion.

   > **Note:** Online expansion can complete without `FileSystemResizePending`. If filesystem expansion requires a pod restart, defer that restart until step 7. Compare storage quantities numerically: `2Gi` and `2048Mi` represent the same capacity.

   Confirm that every claim retains its UID and PV binding, remains `Bound`, and has no deletion timestamp. If controller expansion does not complete, stop and investigate storage events.

3. **Stop the responsible operators.**

   Read each deployment's selector from `operator-before.json`. Set `OP_SELECTOR` to that selector and confirm that it selects only that operator's pods:

   ```sh
   OP_SELECTOR="<operator-pod-label-selector>"
   kubectl --context "$OP_CONTEXT" -n "$OP_NS" scale deployment "$OPERATOR" \
     --current-replicas="$ORIGINAL_REPLICAS" --replicas=0
   kubectl --context "$OP_CONTEXT" -n "$OP_NS" wait --for=delete pod \
     -l "$OP_SELECTOR" --timeout=300s
   kubectl --context "$OP_CONTEXT" -n "$OP_NS" get deployment "$OPERATOR" \
     -o jsonpath='{.spec.replicas}{"\n"}'
   kubectl --context "$OP_CONTEXT" -n "$OP_NS" get pods -l "$OP_SELECTOR"
   ```

   Require zero desired replicas and no remaining operator pods, including terminating pods. If the wait command reports no matching resources, confirm the empty list explicitly.

   Stop every responsible operator before deleting any StatefulSet. With an operator per member, stopping only the central source operator does not pause Search reconciliation.

4. **Orphan-delete the mongot StatefulSets.**

   ```sh
   kubectl --context "$MEMBER_CONTEXT" -n "$NS" delete sts "$STS" \
     --cascade=orphan --wait=true --timeout=300s
   kubectl --context "$MEMBER_CONTEXT" -n "$NS" get sts "$STS"
   ```

   Require `NotFound` while the operator remains stopped. Verify that the existing mongot pods and claims retain their UIDs, the claims retain their PV bindings, and neither has a deletion timestamp.

   > **Important:** Delete only the StatefulSet object. Do not delete the MongoDBSearch resource or its PVCs, reduce mongot replicas, or remove finalizers.

5. **Update the Search storage configuration.**

   Edit the existing Search resource:

   ```sh
   kubectl --context "$CR_CONTEXT" -n "$NS" edit mongodbsearch "$SEARCH"
   ```

   Change only the effective storage size for the affected cluster entries. For example, change the existing persistence configuration to:

   ```yaml
   persistence:
     single:
       storage: 2Gi
       storageClass: <existing-storage-class>
   ```

   This fragment illustrates the field to edit. Keep all other settings, cluster entries, indexes, and replica counts unchanged. If you use shard or StatefulSet overrides, update the field that controls the affected named claim template.

   > **Important:** Do not submit a partial JSON merge patch containing a shortened `spec.clusters` array. The patch replaces the entire array. Removing an entry can trigger cleanup of that cluster's Search resources and claims.

   In central multi-cluster deployments, update the central Search resource. With an operator per member, update the corresponding entries in every local copy. Read the resources back to confirm that the changes persisted and their UIDs remain unchanged.

   Confirm that the targeted StatefulSets remain absent and their pods and claims remain intact. A stale `Running` status while operators are stopped does not demonstrate reconciliation.

6. **Resume the operators.**

   Restore each operator's original replica count:

   ```sh
   kubectl --context "$OP_CONTEXT" -n "$OP_NS" scale deployment "$OPERATOR" \
     --current-replicas=0 --replicas="$ORIGINAL_REPLICAS"
   kubectl --context "$OP_CONTEXT" -n "$OP_NS" rollout status \
     deployment/"$OPERATOR" --timeout=600s
   ```

   Verify that each deployment's desired, updated, and ready replicas equal its recorded count. Do not assume the original count was one.

   Allow up to 10 minutes for each mongot StatefulSet to reappear. Verify that it has a new UID, its original replica count, and the new storage size in the claim template named `data`. Confirm that the existing pods retain their UIDs and their owner references point to the new StatefulSet.

   Confirm that PVC UIDs and PV bindings remain unchanged, claims remain `Bound`, and no deletion timestamps appear. If the retention policy uses `whenDeleted: Delete`, verify that the PVC owner references point to the new StatefulSet UID.

7. **Complete filesystem expansion if required.**

   If online expansion completes, skip pod deletion. If `FileSystemResizePending=True` persists, restart the affected mongot pods one at a time after their StatefulSets adopt them.

   Confirm that the other expected pods are Ready. Set `POD` to an exact pod name from your records:

   ```sh
   POD="<mongot-pod-name>"
   OLD_POD_UID=$(kubectl --context "$MEMBER_CONTEXT" -n "$NS" get pod "$POD" \
     -o jsonpath='{.metadata.uid}')
   kubectl --context "$MEMBER_CONTEXT" -n "$NS" delete pod "$POD" \
     --wait=true --timeout=300s
   kubectl --context "$MEMBER_CONTEXT" -n "$NS" wait --for=condition=Ready \
     pod/"$POD" --timeout=600s
   kubectl --context "$MEMBER_CONTEXT" -n "$NS" get pod "$POD" \
     -o jsonpath='{.metadata.uid}{"\n"}'
   ```

   If the replacement does not yet exist, retry the readiness command within the same 10-minute bound. Require a new pod UID, the same mounted claim, and expanded PVC capacity without a pending filesystem resize. Check your Search query before restarting the next pod.

   > **Important:** Direct pod deletion bypasses PodDisruptionBudgets. A single mongot replica cannot serve requests while it restarts. This procedure does not guarantee zero downtime.

8. **Verify the expanded storage and Search deployment.**

   Confirm that all affected StatefulSets have their expected ready replicas and all expected pods are Ready. Then confirm that each MongoDBSearch resource reports `Running` for its current generation.

   Verify the original PVC UIDs and PV bindings, numeric requests and capacities at least as large as the requested size, and no deletion timestamps. Verify usable filesystem capacity with tools appropriate for your storage backend.

   Run the saved Search query through each affected member's Search path and compare the results. Confirm that new source data becomes searchable after recovery. Successful query retries do not prove uninterrupted service or absence of re-ingestion.

## Troubleshoot Expansion

If expansion fails before you stop the operators, leave them running and inspect CSI events and logs. Do not shrink the request or edit `PV.spec.capacity` to simulate completion.

If a step fails while operators are stopped, record which StatefulSets are absent and which Search updates persisted. Keep the resources, pods, and claims intact. Correct the effective storage settings and restore every paused operator to its recorded replica count.

If you must resume before correcting the Search configuration, the recreated StatefulSet can retain the old template size. Treat the procedure as incomplete and repeat the controlled stop, orphan-delete, update, and resume sequence. Do not shrink or delete the expanded claim.

If a retained pod or claim has a deletion timestamp, stop further deletions and contact your Kubernetes or storage administrator. Resuming an operator does not cancel Kubernetes garbage collection. Do not remove finalizers as a recovery shortcut.
