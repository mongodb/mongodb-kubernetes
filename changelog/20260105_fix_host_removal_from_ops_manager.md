---
kind: fix
date: 2026-01-05
---

* **MongoDBMultiCluster**, **MongoDB**: Fix an issue where the operator skipped host removal when an external domain was used, leaving monitoring hosts in Ops Manager even after workloads were correctly removed from the cluster.
