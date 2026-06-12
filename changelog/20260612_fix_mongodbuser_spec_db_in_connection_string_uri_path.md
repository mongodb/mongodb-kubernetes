---
kind: fix
date: 2026-06-12
---

* **MongoDBUser**: The database in generated connection string secrets now includes `spec.db` in the URI path (e.g. `mongodb://host/mydb`). Previously the path was always empty, causing the MongoDB driver to default to the `test` database.
