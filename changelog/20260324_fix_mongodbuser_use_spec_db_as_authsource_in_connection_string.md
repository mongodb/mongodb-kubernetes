---
kind: fix
date: 2026-03-24
---

* **MongoDBUser**: `authSource` in the generated connection string secret now correctly reflects `spec.db` instead of being hardcoded to `admin`.
