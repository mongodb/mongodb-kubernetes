---
kind: feature
date: 2026-09-29
---

* **MongoDBCommunity**: Connection strings are now built by the same shared builder used by the enterprise resources. Community user connection string secrets follow the enterprise behavior: the user's database is passed as `authSource`, the URI path comes from the new `connectionStringDatabase` field, and the `authMechanism` parameter is derived for SCRAM deployments. Parameters in generated URIs are now sorted deterministically and include the operator defaults `connectTimeoutMS` and `serverSelectionTimeoutMS`. Only `replicaSet` and `ssl` are protected from being overridden through `additionalConnectionStringConfig`; previously `tls` was filtered as well.
* **MongoDBCommunity**: `MongoDBUser` gains the `connectionStringDatabase` field, matching the enterprise resource, to set the database in the connection string URI path. Generated connection string secret names for `$external` users no longer contain a double dash: the `$` prefix of the database is trimmed like the enterprise resource does.
* **MongoDBUser**: The `db` field is no longer required and defaults to `admin` through the CRD schema.
* **MongoDBOpsManager**: The connection strings built for S3 backup stores and backup datastores now set `authSource` to the database of the referenced `MongoDBUser` instead of always deriving `admin`, matching the behavior of the user connection string secrets.
* **MongoDBCommunity**: The per user connection string secrets no longer contain a `password` field for `$external` users (the field was previously written empty), matching the enterprise behavior. The deprecated `tls` parameter remains protected in `additionalConnectionStringConfig` and user options alongside `ssl`: generated connection strings only ever contain the `ssl` parameter.
