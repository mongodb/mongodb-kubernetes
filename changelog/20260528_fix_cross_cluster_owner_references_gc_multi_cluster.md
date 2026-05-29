---
kind: fix
date: 2026-05-28
---

* **MongoDBOpsManager**, **MongoDB**, **MongoDBMultiCluster**, **MongoDBUser**: Fixed a bug in multi-cluster mode where Kubernetes resources created in remote member clusters carried `ownerReferences` pointing to the Custom Resource in the central cluster.
* **MongoDBUser**: Fixed a bug where the connection string `Secret` created in the central cluster did not carry a controller owner reference to the `MongoDBUser` CR. The missing reference prevented Kubernetes garbage collection from cleaning up the `Secret` when the `MongoDBUser` was deleted.
* **MongoDBUser**: Fixed a bug where the ownership guard on the connection string `Secret` was always satisfied regardless of the actual owner, allowing the operator to silently overwrite a `Secret` controlled by a different resource.
* **MongoDB**: Fixed two bugs in `additionalMongodConfig` field removal. Setting a field to `null` (to remove it) was not propagating correctly to Ops Manager because (1) `maputil.traverse` treated `nil` as a present terminal value rather than an absent one, and (2) `MergeMaps` would re-introduce the `nil` value back into the process map on subsequent reconciliations instead of deleting the key.
