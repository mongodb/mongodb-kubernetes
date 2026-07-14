---
kind: fix
date: 2026-05-27
---

* **MongoDBOpsManager**: Fixed an issue in the `MongoDBOpsManager` resource where JVM parameter blocks were appended to `mms.conf` on every pod restart without removing previous entries, causing duplicate configuration entries to accumulate.
