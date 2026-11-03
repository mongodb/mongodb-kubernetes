---
kind: fix
date: 2026-11-03
---

* **MongoDB**, **MongoDBMultiCluster**, **MongoDBUser**: Fixed a bug where a concurrent Ops Manager automation config update marked the resource as Failed. The operator now reports Pending for `AUTOMATION_CONFIG_CONCURRENT_MODIFICATION` and retries. Other errors keep their own status: pending waits stay Pending and hard failures stay Failed.
