---
kind: breaking
date: 2026-08-05
---

* **MongoDBUser**: Removed `spec.db` in favor of two independent fields: `spec.authSource`, which defines the database used for authentication, and `spec.defaultDatabase`, which defines the database segment of the connection string URI path. `spec.authSource` defaults to `admin` when unset. `spec.defaultDatabase` is not defaulted: if left unset, the URI path stays empty and the MongoDB driver falls back to its own default database (`test`). `MongoDBUser` resources still using `spec.db` must migrate to `spec.authSource` and `spec.defaultDatabase` before upgrading.
* **MongoDBCommunity**: Removed `spec.users[].db` in favor of two independent fields: `spec.users[].authSource`, which defines the database used for authentication, and `spec.users[].defaultDatabase`, which defines the database segment of the connection string URI path. `spec.users[].authSource` defaults to `admin`, matching the previous default of `spec.users[].db`. `spec.users[].defaultDatabase` is not defaulted: unlike `MongoDBUser`, `MongoDBCommunity` connection strings previously always included a database in the URI path (from `spec.users[].db`), so users who want that path populated must now set `spec.users[].defaultDatabase` explicitly. Users still using `spec.users[].db` must migrate to `spec.users[].authSource` and `spec.users[].defaultDatabase` before upgrading.
