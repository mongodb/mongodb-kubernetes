---
kind: fix
date: 2025-12-24
---

* *MongoDBMultiCluster*: Fix an issue where the operator skipped host removal when an external domain was used, leaving monitoring hosts in Ops Manager even after workloads were correctly removed from the cluster.
