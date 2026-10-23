---
kind: fix
date: 2026-10-23
---

* **ReplicaSet**: Fixed a bug where deleting a SCRAM-enabled `ReplicaSet` could leave `deploymentAuthMechanisms` set in the Ops Manager project. A concurrent reconciliation could re-add the replica set while deletion waited for agent readiness, so the authentication reset was skipped. The same delete/reconcile serialization now applies to sharded clusters, standalone deployments, and multi-cluster replica sets.
