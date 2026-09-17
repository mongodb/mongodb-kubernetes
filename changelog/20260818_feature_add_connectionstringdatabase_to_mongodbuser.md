---
kind: feature
date: 2026-08-18
---

* **MongoDBUser**: Added optional `spec.connectionStringDatabase` to populate the database segment of the connection string URI path. `spec.db` continues to set the `authSource` parameter. For example, with `spec.db` of `admin` and `spec.connectionStringDatabase` of `myapp`, the generated secret is `mongodb://user:pass@host/myapp?authSource=admin&...`.
