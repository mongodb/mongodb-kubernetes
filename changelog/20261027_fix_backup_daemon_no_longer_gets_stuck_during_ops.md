---
kind: fix
date: 2026-10-27
---

* **MongoDBOpsManager**: Fixed an issue where changing `spec.version` with backup enabled could leave the Backup Daemon pod stuck, so the resource never reached the `Running` phase after the upgrade. The operator now stops the Backup Daemon and waits for its pod to terminate before upgrading Ops Manager, and removes any Backup Daemon pod that is still running the old version once Ops Manager is upgraded.
