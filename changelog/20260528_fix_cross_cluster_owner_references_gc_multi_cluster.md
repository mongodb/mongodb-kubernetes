---
kind: fix
date: 2026-05-28
---

* **MongoDBOpsManager**, **MongoDB**, **MongoDBMultiCluster**, **MongoDBUser**: Fixed a bug in multi-cluster mode where Kubernetes resources created in remote member clusters carried `ownerReferences` pointing to the Custom Resource in the central cluster.
* **MongoDBUser**: Fixed a bug where the connection string `Secret` created in the central cluster did not carry a controller owner reference to the `MongoDBUser` CR. The missing reference prevented Kubernetes garbage collection from cleaning up the `Secret` when the `MongoDBUser` was deleted.
* **MongoDBUser**: Fixed a bug where the ownership guard on the connection string `Secret` was always satisfied regardless of the actual owner, allowing the operator to silently overwrite a `Secret` controlled by a different resource.
* **MongoDB**: Fixed a bug where setting a field to `null` in `additionalMongodConfig` to remove it from a deployment did not take effect. The field would either be ignored or reappear on the next reconciliation.
