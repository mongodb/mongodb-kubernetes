---
kind: fix
date: 2026-11-03
---

* **MongoDB**, **MongoDBMultiCluster**, **MongoDBUser**: Fixed a bug where a concurrent Ops Manager automation config update marked the resource as Failed. The operator now reports Pending and retries, because another writer is expected to change the config.
