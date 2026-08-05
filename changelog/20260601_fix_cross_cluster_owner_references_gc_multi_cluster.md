---
kind: fix
date: 2026-06-01
---

* **MongoDBOpsManager**, **MongoDB**, **MongoDBMultiCluster**, **MongoDBUser**: Fixed a bug in multi-cluster mode where `Service`, `ConfigMap`, TLS `Secret`, and `StatefulSet` resources created in member clusters carried `ownerReferences` pointing to the Custom Resource in the central cluster, causing the Kubernetes garbage collector to delete them as orphans.
* **MongoDB**, **MongoDBMultiCluster**, **MongoDBOpsManager**, **AppDB**: Fixed a bug where reconciliation did not consistently trigger after StatefulSet resource changes in single-cluster and multi-cluster deployments.
