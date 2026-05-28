---
kind: fix
date: 2026-05-28
---

* **MongoDBOpsManager**, **MongoDB**, **MongoDBMultiCluster**, **MongoDBUser**: Fixed a bug in multi-cluster mode where Kubernetes resources created in remote member clusters carried `ownerReferences` pointing to the Custom Resource in the central cluster.
