---
kind: breaking
date: 2026-08-05
---

* **MongoDBUser**: The generated connection string secrets now include the database in the URI path (e.g. `mongodb://host/mydb`). Previously the path was always empty, causing the MongoDB driver to default to the `test` database. Existing clients relying on the empty path defaulting to `test` must update their connection handling.
* **MongoDBUser**: Added `spec.authSource` and `spec.defaultDatabase` fields, replacing the deprecated `spec.db` field. `spec.authSource` sets the authentication database (the `authSource` query parameter in the connection string, e.g. `mongodb://host/mydb?authSource=admin`) and `spec.defaultDatabase` sets the database placed in the URI path (`mydb` in that example). `spec.authSource` and `spec.defaultDatabase` must be set together.
