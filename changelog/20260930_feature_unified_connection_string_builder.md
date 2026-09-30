---
kind: feature
date: 2026-09-30
---

* **MongoDBCommunity**: Connection strings are now built by the same shared builder used by the enterprise resources, and `MongoDBUser` gains the `connectionStringDatabase` field, matching the enterprise resource, to set the database in the connection string URI path.
* **MongoDBUser**: The `db` field is no longer required and defaults to `admin` through the CRD schema.
