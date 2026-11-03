---
kind: fix
date: 2026-10-27
---

* **MongoDBOpsManager**: Fixed an issue where changing `spec.version` with backup enabled could leave the Backup Daemon pod stuck, so the resource never reached the `Running` phase. The Backup Daemon is now stopped before Ops Manager is upgraded.
