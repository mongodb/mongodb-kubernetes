---
title: clusterSpecList validation fix
kind: fix
date: 2025-07-02
---

* **MongoDB**, **MongoDBMultiCluster**, **MongoDBOpsManager**: In case of losing one of the member clusters we no longer emit validation errors if the failed cluster still exists in the `clusterSpecList`. This allows easier reconfiguration of the deployments as part of disaster recovery procedure.
