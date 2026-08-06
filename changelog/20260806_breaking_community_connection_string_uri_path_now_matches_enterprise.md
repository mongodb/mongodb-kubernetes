---
kind: breaking
date: 2026-08-06
---

* **MongoDBCommunity**: The database segment of the connection string URI path is no longer populated from `spec.users[].db`. Previously Community connection string secrets always included the auth database in the URI path (for example `mongodb://.../admin?authSource=admin`), while `MongoDBUser` never did. Both resources now behave the same way: the URI path stays empty unless the new `connectionStringDatabase` field is set, and the MongoDB driver falls back to its own default database. Set `spec.users[].connectionStringDatabase` to restore a populated path.

  `spec.users[].db` keeps its meaning and its `admin` default. It continues to define the authentication database, and still drives the `authSource` connection string parameter and the Automation Config user.

* **MongoDBCommunity**: Connection strings now always carry an `authSource` parameter derived from `spec.users[].db`. As a result, `authSource` is no longer read from `spec.additionalConnectionStringConfig` or `spec.users[].additionalConnectionStringConfig`, which prevents the parameter being written twice. Anyone who previously set `authSource` through either of those maps with a value different from `spec.users[].db` must now set `spec.users[].db` instead.

* **MongoDBUser** and **MongoDBCommunity**: Added the optional `connectionStringDatabase` field, which controls only the database segment of the connection string URI path. It has no default. It is ignored when the auth database is `$external`, since that is an auth only pseudo database and must not appear in the URI path.
