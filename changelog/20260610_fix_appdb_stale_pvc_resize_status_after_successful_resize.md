---
kind: fix
date: 2026-06-10
---

* **OpsManager AppDB**: Fixed an issue where the `status.applicationDatabase.pvc` field in the `MongoDBOpsManager` CRD retained a stale `PVC Resize - STS has been orphaned` phase indefinitely after a PVC resize completed successfully.
