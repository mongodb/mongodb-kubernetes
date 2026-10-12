# Manually Expand MongoDB Search Index Storage

You can increase mongot index storage by expanding its persistent volume claims (PVCs). This procedure stops the Kubernetes Operator while you replace the StatefulSet definition, then resumes the operator with the existing pods and claims.

For other MongoDB resources, see [Increase Storage for Persistent Volumes](https://www.mongodb.com/docs/kubernetes/current/tutorial/resize-pv-storage/).

## Prerequisites

- Use a StorageClass with `allowVolumeExpansion: true` and a CSI driver that supports expansion while the volume remains attached. Confirm that the filesystem supports expansion. This procedure does not cover backends that require detaching the volume for controller expansion.
- Start with a healthy source database and Search deployment, and prepare a backup or recovery plan.
- Schedule a maintenance window without concurrent scaling, upgrades, or GitOps changes. Stopping an operator pauses reconciliation for all resources it manages.
- Obtain permission to patch PVCs, edit MongoDBSearch resources, scale operator deployments, and delete StatefulSets and pods.

> **Important:** Increase storage only. Search StatefulSets use `whenDeleted: Delete` and `whenScaled: Delete` PVC retention policies beginning with Kubernetes Operator 1.9. Stopping the operator does **not** stop Kubernetes garbage collection. Keep the existing Search resource and PVCs. Do not change mongot replica counts. Delete only the StatefulSet, with `--cascade=orphan`. A PV reclaim policy of `Retain` does not prevent PVC deletion.

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

1. **Identify the data claims.**

   Inspect each mongot pod to identify the PVC mounted by its `data` volume:

   ```sh
   kubectl --context <member-context> -n <namespace> \
     describe pod <mongot-pod-name>
   ```

   Record each exact claim name, UID, and PV binding. Do not select claims by a name prefix. Save a representative Search query and its expected results.

   ```sh
   kubectl --context <member-context> -n <namespace> get pvc <pvc-name> \
     -o jsonpath='{.metadata.uid} {.spec.volumeName}{"\n"}'
   ```

2. **Expand each data claim.**

   Run this command for each claim identified in step 1:

   ```sh
   kubectl --context <member-context> -n <namespace> patch pvc <pvc-name> \
     --type=merge \
     -p '{"spec":{"resources":{"requests":{"storage":"2Gi"}}}}'
   ```

   Inspect the claim and its bound PV:

   ```sh
   kubectl --context <member-context> -n <namespace> get pvc <pvc-name> -o yaml
   kubectl --context <member-context> get pv <pv-name> -o yaml
   ```

   Wait for the PV capacity to reach the requested size. The PVC must either report that capacity or report `FileSystemResizePending=True` after controller expansion. Online expansion can complete without that condition. Compare quantities numerically: `2Gi` equals `2048Mi`.

   Keep the claim `Bound` with no deletion timestamp. If expansion exceeds your storage backend's documented timeout, investigate CSI events before proceeding.

3. **Stop the responsible operator deployments.**

   Record each operator's original replica count:

   ```sh
   kubectl --context <operator-context> -n <operator-namespace> \
     get deployment <operator-name> -o jsonpath='{.spec.replicas}{"\n"}'
   ```

   Scale the operator, **not mongot**, to zero. Use the deployment's pod selector to wait for its pods to stop:

   ```sh
   kubectl --context <operator-context> -n <operator-namespace> \
     scale deployment <operator-name> --replicas=0
   kubectl --context <operator-context> -n <operator-namespace> \
     wait --for=delete pod -l <operator-pod-selector> --timeout=300s
   kubectl --context <operator-context> -n <operator-namespace> \
     get pods -l <operator-pod-selector>
   ```

   Confirm that no operator pods remain, including terminating pods. Stop all responsible operators before continuing. Stopping a separate source-database operator does not pause member Search operators.

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
       storageClass: <existing-storage-class>
   ```

   Keep the existing storage class, cluster entries, indexes, replicas, and other settings. Do not replace the `clusters` array with a partial JSON merge patch: removing an entry can trigger deletion of that cluster's Search resources and claims.

   Read the Search resources back to confirm that the new effective sizes persisted. The targeted StatefulSets must remain absent until you resume the operators.

6. **Resume the operators.**

   Restore each operator's own recorded replica count:

   ```sh
   kubectl --context <operator-context> -n <operator-namespace> \
     scale deployment <operator-name> --replicas=<original-replicas>
   kubectl --context <operator-context> -n <operator-namespace> \
     rollout status deployment/<operator-name> --timeout=600s
   ```

   Wait for each StatefulSet to reappear with a new UID, its original replica count, and the expanded template named `data`. Confirm that it adopts the existing pods and claims before proceeding. Do not treat a stale Search `Running` status from the paused period as completion.

7. **Restart pods only if filesystem expansion requires it.**

   If `FileSystemResizePending=True` persists, delete the affected mongot pods **one at a time**:

   ```sh
   kubectl --context <member-context> -n <namespace> \
     delete pod <mongot-pod-name> --timeout=300s
   kubectl --context <member-context> -n <namespace> \
     wait --for=condition=Ready pod/<mongot-pod-name> --timeout=600s
   ```

   Wait for the replacement pod to exist before running the readiness command. Confirm that it mounts the same claim, the PVC reports expanded capacity, and the pending condition clears. Check the Search query before restarting another pod. Skip this step if expansion completes online.

   > **Note:** Direct pod deletion bypasses PodDisruptionBudgets. A single mongot replica cannot serve requests while it restarts. This procedure does not guarantee zero downtime.

8. **Verify storage and Search readiness.**

   Repeat the claim identity command from step 1 and compare the UID and PV binding. Require the same claims, `Bound`, no deletion timestamps, and requests and capacities at least as large as the new size. Verify usable filesystem capacity with your storage backend's tools.

   Confirm that all expected mongot pods are Ready and each Search resource reaches `Running` after reconciliation. Run your representative Search query through each affected member's Search path and compare the results.

## Troubleshoot Expansion

If a step fails while operators are stopped, keep the Search resources, pods, and claims intact. Correct the effective storage settings, then restore every paused operator to its original replica count. If you resume before correcting the configuration, complete the procedure later rather than shrinking or deleting a claim.

If a pod or claim starts deleting, stop further deletions and contact your Kubernetes or storage administrator. Do not remove finalizers or edit `PV.spec.capacity` as a workaround.
