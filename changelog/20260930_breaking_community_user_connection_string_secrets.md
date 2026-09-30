---
kind: breaking
date: 2026-09-30
---

* **MongoDBCommunity**: The generated connection string secret names for users change order from `<resource>-<database>-<username>` to `<resource>-<username>-<database>`, matching the enterprise resource, and no longer contain a double dash for `$external` users since the `$` prefix of the database is trimmed. The new name is reflected in the resource status, but any consumer mounting the secret by the old hardcoded name must switch to the new one or read the name from the status.
* **MongoDBCommunity**: The per user connection string secrets no longer contain a `password` field for `$external` users (the field was previously written empty), matching the enterprise behavior.
* **MongoDBCommunity**: User connection string URIs change: the user's database is now passed as `authSource`, the `authMechanism` parameter is derived for SCRAM deployments, the operator defaults `connectTimeoutMS` and `serverSelectionTimeoutMS` are always included, and parameters are sorted deterministically.
