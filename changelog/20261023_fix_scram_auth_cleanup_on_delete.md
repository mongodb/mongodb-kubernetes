---
kind: fix
date: 2026-10-23
---

* **ReplicaSet**: Fixed an issue where deleting a SCRAM-enabled replica set could fail to remove its authentication settings from Ops Manager. The operator now adds a finalizer to MongoDB and MongoDBMultiCluster resources and retries cleanup until it succeeds. The same fix applies to sharded clusters, standalone deployments, and multi-cluster replica sets.
  * While cleanup is pending, the resource stays in `Terminating`, and a new resource with the same name cannot be created until the old one is gone.
  * If cleanup can never succeed, for example because the Ops Manager project or its API key no longer exists, remove the finalizer to finish the deletion without cleanup: `kubectl patch mdb <name> --type=merge -p '{"metadata":{"finalizers":null}}'`. Alternatively, set the annotation `mongodb.com/disable-reconciliation: "true"` on the resource, and the operator removes the finalizer without cleaning up Ops Manager.
  * When an active backup is kept on deletion (`spec.backup.autoTerminateOnDeletion: false`), Ops Manager keeps monitoring the last host of each backed-up replica set to anchor the backup. The operator removes all other hosts, and retries if a host removal fails for any other reason.
