---
kind: breaking
date: 2026-08-05
---

* **MongoDBUser**: Removed `spec.db` and replaced it with `spec.authSource` and `spec.defaultDatabase`. `spec.authSource` sets the authentication database (the `authSource` query parameter in the connection string, e.g. `mongodb://host/mydb?authSource=admin`) and `spec.defaultDatabase` sets the database placed in the URI path (`mydb` in that example). Both fields must be set together, and default to `admin` when neither is set. Any `MongoDBUser` still using `spec.db` must migrate to the new fields before upgrading.
* **MongoDBUser**: The generated connection string secrets now always include a database in the URI path (`admin` by default, or `spec.defaultDatabase` when set). Previously the path was always empty, causing the MongoDB driver to default to the `test` database. This changes the runtime behavior of every existing `MongoDBUser` connection string.
