---
kind: breaking
date: 2026-08-05
---

* **MongoDBUser**: Removed `spec.db` in favor of two explicit fields: `spec.authSource`, which defines the database used for authentication, and `spec.defaultDatabase`, which defines the database segment of the connection string URI path. Both fields are required together and default to `admin` when neither is specified. `MongoDBUser` resources still using `spec.db` must migrate to `spec.authSource` and `spec.defaultDatabase` before upgrading.
* **MongoDBUser**: Generated connection string secrets now always include a database in the URI path (`spec.defaultDatabase`, or `admin` by default). Previously this segment was always empty, causing MongoDB drivers to default to the `test` database. This changes the runtime behavior of every existing `MongoDBUser` connection string.
* **MongoDBCommunity**: Removed `spec.users[].db` in favor of two explicit fields: `spec.users[].authSource`, which defines the database used for authentication, and `spec.users[].defaultDatabase`, which defines the database segment of the connection string URI path. Both fields default to `admin` when neither is specified, matching the previous default of `spec.users[].db`. Unlike `MongoDBUser`, `MongoDBCommunity` connection strings already included a database in the URI path (from `spec.users[].db`), so this change does not alter existing connection string output; it only changes the field names. Users still using `spec.users[].db` must migrate to `spec.users[].authSource` and `spec.users[].defaultDatabase` before upgrading.
