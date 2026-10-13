# Manually Expand MongoDB Search Index Storage

The Kubernetes Operator does not automatically expand MongoDB Search storage. For Kubernetes Operator 1.9 or later, expand the mongot persistent volume claims (PVCs) manually using this procedure. Stop the operator while you replace the StatefulSet definitions, then resume it with the existing pods and claims.

For other MongoDB resources, see [Increase Storage for Persistent Volumes](https://www.mongodb.com/docs/kubernetes/current/tutorial/resize-pv-storage/).

## Prerequisites

- Use a StorageClass with `allowVolumeExpansion: true` and a CSI driver that supports expansion while the volume remains attached. Confirm that the filesystem supports expansion. This procedure does not cover backends that require detaching the volume for controller expansion.
- Start with a healthy source database and Search deployment, and prepare a backup or recovery plan.
- Schedule a maintenance window without concurrent scaling or upgrades. Pause automation, such as GitOps sync, that can restore operator replicas or revert the Search storage setting. If you manage the Search resource in Git, commit the new size before resuming sync. Stopping an operator pauses reconciliation for all resources it manages.
- Obtain permission to patch PVCs, edit MongoDBSearch resources, scale operator deployments, and delete StatefulSets and pods.

> **Important:** Increase storage only. Keep the existing Search resource and PVCs, and do not scale mongot down. Delete only StatefulSet definitions with `--cascade=orphan`: their PVC retention policy remains active while the operator is stopped.

### Multi-Cluster Deployments

Use the appropriate context for each command:

| Deployment | Search resource context | StatefulSet and PVC context | Operator to stop and resume |
| --- | --- | --- | --- |
| Single cluster | Workload cluster | Same cluster | Local operator |
| Central multi-cluster operator | Central cluster | Each affected member | Central operator |
| Operator per member | Each member's local Search copy | Same member | Each affected member's Search operator |

Complete each step for all affected StatefulSets before continuing. With an operator per member, update the corresponding settings in every local Search copy.

## Manually Expand Storage

This example expands replica-set Search storage from `1Gi` to `2Gi`. Match each StatefulSet to its cluster entry's `name` and explicit `index`, not the entry's array position.

> **Note:** A matching `shardOverrides[].persistence` replaces cluster persistence. Custom `statefulSet.spec.volumeClaimTemplates` can override the generated storage size. If you use either setting, identify and update the effective storage field for each affected StatefulSet instead of applying this example unchanged.

1. **Identify the StatefulSets and data claims.**

   List the mongot StatefulSets for the Search resource:

   ```sh
   kubectl --context <member-context> -n <namespace> \
     get sts -l mongodb.com/search-name=<search-name>
   ```

   With the default `data` claim template, each replica uses `data-<statefulset-name>-<ordinal>`. For sharded sources, each cluster has one StatefulSet per shard. For customized templates or mounts, check the claim mounted by each pod instead.

2. **Expand each data claim.**

   Run this command for each claim identified in step 1:

   ```sh
   kubectl --context <member-context> -n <namespace> patch pvc <pvc-name> \
     --type=merge \
     -p '{"spec":{"resources":{"requests":{"storage":"2Gi"}}}}'
   ```

   Wait until each claim reports at least the requested size in `status.capacity.storage` or the `FileSystemResizePending=True` condition:

   ```sh
   kubectl --context <member-context> -n <namespace> get pvc <pvc-name> -o yaml
   ```

   If expansion exceeds your storage backend's documented timeout, investigate CSI events before proceeding.

3. **Stop the responsible operator deployments.**

   Record each operator's original replica count:

   ```sh
   kubectl --context <operator-context> -n <operator-namespace> \
     get deployment <operator-name> -o jsonpath='{.spec.replicas}{"\n"}'
   ```

   Scale the operator, **not mongot**, to zero, and wait for its pods to stop:

   ```sh
   kubectl --context <operator-context> -n <operator-namespace> \
     scale deployment <operator-name> --replicas=0
   kubectl --context <operator-context> -n <operator-namespace> \
     wait --for=delete pod --timeout=300s \
     -l app.kubernetes.io/name=<operator-name>
   ```

   For customized installations, use the Deployment's actual pod selector instead of the Helm chart label shown here. Stop all responsible operator pods, including terminating pods, before continuing. Stopping a separate source-database operator does not pause member Search operators.

4. **Remove only the StatefulSet definitions.**

   ```sh
   kubectl --context <member-context> -n <namespace> \
     delete sts <statefulset-name> --cascade=orphan --timeout=300s
   ```

   Confirm that each StatefulSet is absent while its existing mongot pods and PVCs remain intact, without deletion timestamps. Do not delete the MongoDBSearch resource or scale mongot down.

5. **Update the existing Search resources.**

   ```sh
   kubectl --context <search-resource-context> -n <namespace> \
     edit mongodbsearch <search-name>
   ```

   Change the affected cluster entry's `persistence.single.storage` value:

   ```yaml
   persistence:
     single:
       storage: 2Gi
   ```

   Keep the existing storage class, cluster entries, indexes, replicas, and other settings. Removing a cluster entry can delete that cluster's Search resources and claims.

   Read the Search resources back to confirm that the new effective sizes persisted. The targeted StatefulSets must remain absent until you resume the operators.

6. **Resume the operators.**

   Restore each operator's own recorded replica count:

   ```sh
   kubectl --context <operator-context> -n <operator-namespace> \
     scale deployment <operator-name> --replicas=<original-replicas>
   kubectl --context <operator-context> -n <operator-namespace> \
     rollout status deployment/<operator-name> --timeout=600s
   ```

   Wait for each StatefulSet to reappear with the expanded `data` claim template. The new StatefulSet adopts the existing pods and claims.

7. **Restart pods only if filesystem expansion requires it.**

   If `FileSystemResizePending=True` persists, delete the affected mongot pods **one at a time**:

   ```sh
   kubectl --context <member-context> -n <namespace> \
     delete pod <mongot-pod-name> --timeout=300s
   kubectl --context <member-context> -n <namespace> \
     wait --for=condition=Ready pod/<mongot-pod-name> --timeout=600s
   ```

   Wait for the replacement pod to exist before running the readiness command. Restart the next pod only after the replacement is Ready and the pending condition clears. Skip this step if expansion completes online.

   > **Note:** Direct pod deletion bypasses PodDisruptionBudgets. A single mongot replica cannot serve requests while it restarts. This procedure does not guarantee zero downtime.

8. **Verify storage and Search readiness.**

   Confirm that each PVC reports at least the requested capacity, all expected mongot pods are Ready, and each Search resource reaches `Running` after reconciliation.

## Troubleshoot Expansion

If a step fails while operators are stopped, keep the Search resources, pods, and claims intact. Correct the effective storage settings, then restore every paused operator to its original replica count. If you resume before correcting the configuration, complete the procedure later rather than shrinking or deleting a claim.

If a pod or claim starts deleting, stop further deletions and contact your Kubernetes or storage administrator. Do not remove finalizers or edit `PV.spec.capacity` as a workaround.
