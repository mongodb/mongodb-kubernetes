---
kind: fix
date: 2026-06-01
---

* **MongoDBOpsManager**, **MongoDB**, **MongoDBMultiCluster**: Fixed a bug in multi-cluster mode where `StatefulSet` resources created in remote member clusters carried `ownerReferences` pointing to the Custom Resource in the central cluster, causing the Kubernetes garbage collector to delete them as orphans. Member-cluster StatefulSets now carry the `MongoDBMultiResource` annotation instead, which the operator uses to map them back to their parent Custom Resource.
