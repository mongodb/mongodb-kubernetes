---
kind: fix
date: 2026-07-29
---

* **MongoDBOpsManager**: Fixed a bug where the Operator could briefly show a false `Failed` status with the message "The admin-key secret might be corrupted" during an Ops Manager upgrade while the Application Database restarts. The status now remains stable during this transient period.
