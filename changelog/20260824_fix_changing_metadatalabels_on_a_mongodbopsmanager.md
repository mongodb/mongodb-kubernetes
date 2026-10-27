---
kind: fix
date: 2026-08-24
---

* Changing `metadata.labels` on a `MongoDBOpsManager` resource no longer leaves it stuck in `Failed` state. The labels at the `MongoDBOpsManager` level are no longer propagating to the `PersistentVolumeClaims` of the AppDB and Backup Daemon StatefulSets.
