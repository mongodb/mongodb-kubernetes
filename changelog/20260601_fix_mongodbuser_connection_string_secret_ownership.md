---
kind: fix
date: 2026-06-01
---

* **MongoDBUser**: Fixed a bug where the connection string `Secret` created in the central cluster did not carry a controller owner reference to the `MongoDBUser` CR. The missing reference prevented Kubernetes garbage collection from cleaning up the `Secret` when the `MongoDBUser` was deleted.
* **MongoDBUser**: Fixed a bug where the ownership guard on the connection string `Secret` was always satisfied regardless of the actual owner, allowing the operator to silently overwrite a `Secret` controlled by a different resource.
