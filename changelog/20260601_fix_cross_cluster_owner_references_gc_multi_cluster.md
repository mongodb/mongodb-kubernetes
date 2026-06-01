---
kind: fix
date: 2026-06-01
---

* **MongoDBOpsManager**, **MongoDB**, **MongoDBMultiCluster**, **MongoDBUser**: Fixed a bug in multi-cluster mode where `Service`, `ConfigMap`, and TLS `Secret` resources created in remote member clusters carried `ownerReferences` pointing to the Custom Resource in the central cluster, causing the Kubernetes garbage collector to delete them as orphans.
