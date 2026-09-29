---
kind: feature
date: 2026-09-29
---

* **MongoDBCommunity**: Connection strings are now built by the same shared builder used by the enterprise resources. Community user connection string secrets follow the enterprise behavior: the user's database is passed as `authSource`, the URI path defaults to the `admin` database, and the `authMechanism` parameter is derived for SCRAM deployments. Parameters in generated URIs are now sorted deterministically and include the operator defaults `connectTimeoutMS` and `serverSelectionTimeoutMS`. Only `replicaSet` and `ssl` are protected from being overridden through `additionalConnectionStringConfig`; previously `tls` was filtered as well.
* **MongoDBUser**: The `db` field is no longer required and defaults to `admin` through the CRD schema.
