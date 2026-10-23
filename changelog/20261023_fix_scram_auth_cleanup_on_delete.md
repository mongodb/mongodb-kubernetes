---
kind: fix
date: 2026-10-23
---

* **ReplicaSet**: Fixed a bug where deleting a SCRAM-enabled `ReplicaSet` could leave `deploymentAuthMechanisms` set in the Ops Manager project. A concurrent reconciliation could re-add the replica set after cleanup, and the single delete attempt did not retry. Deletion cleanup now runs in the reconcile loop through a finalizer, so the workqueue serializes it with the in-flight reconcile and retries a failed cleanup. The same finalizer applies to sharded clusters, standalone deployments, and multi-cluster replica sets.
