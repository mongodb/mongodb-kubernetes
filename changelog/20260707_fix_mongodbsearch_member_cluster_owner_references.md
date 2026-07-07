---
kind: fix
date: 2026-07-07
---

* **MongoDBSearch**: Fixed a bug in multi-cluster mode where `StatefulSet`, `Service`, `ConfigMap`, and TLS `Secret` resources created in member clusters carried `ownerReferences` pointing to the `MongoDBSearch` resource in the central cluster, causing the Kubernetes garbage collector to delete them immediately. These resources are now tracked with operator labels instead.
