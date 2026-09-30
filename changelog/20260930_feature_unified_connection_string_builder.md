---
kind: feature
date: 2026-09-30
---

* **MongoDBCommunity**: Connection strings are now built by the same shared builder used by the enterprise resources. Community user connection string secrets follow the enterprise behavior: the user's database is passed as `authSource`, the URI path comes from the new `connectionStringDatabase` field, and the `authMechanism` parameter is derived for SCRAM deployments. Parameters in generated URIs are now sorted deterministically and include the operator defaults `connectTimeoutMS` and `serverSelectionTimeoutMS`.
* **MongoDBCommunity**: `MongoDBUser` gains the `connectionStringDatabase` field, matching the enterprise resource, to set the database in the connection string URI path.
* **MongoDBCommunity**: The generated connection string secret names for users change order from `<resource>-<database>-<username>` to `<resource>-<username>-<database>`, matching the enterprise resource, and no longer contain a double dash for `$external` users since the `$` prefix of the database is trimmed. The new name is reflected in the resource status, but any consumer mounting the secret by the old hardcoded name must switch to the new one or read the name from the status.
* **MongoDBCommunity**: Parameters supplied through `additionalConnectionStringConfig` or user options now override the ones derived from the resource instead of being filtered. The operator itself always emits the `ssl` parameter; a `tls` value supplied by a caller is passed through unchanged.
* **MongoDBUser**: The `db` field is no longer required and defaults to `admin` through the CRD schema.
