---
kind: fix
date: 2026-07-16
---

* **MongoDBOpsManager**: Fixed Ops Manager failing to start after TLS is disabled when the configuration directory is persisted (for example a PVC mounted over `/mongodb-ops-manager/conf`). Properties the operator no longer sets, such as `mms.https.PEMKeyFile`, are now removed from `conf-mms.properties` on container start instead of lingering forever.
