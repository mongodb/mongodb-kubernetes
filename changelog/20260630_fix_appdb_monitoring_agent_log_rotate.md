---
kind: fix
date: 2026-06-30
---

* **MongoDBOpsManager**, **AppDB**: Fix for `spec.applicationDatabase.agent.monitoringAgent.logRotate` field not being handled properly. Additionally, added defaults for `spec.applicationDatabase.agent.monitoringAgent.logRotate.sizeThresholdMB` to 1000 and `spec.applicationDatabase.agent.monitoringAgent.logRotate.timeThresholdHrs` to 24, which are same defaults Ops Manager would set.
