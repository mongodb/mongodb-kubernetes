---
kind: fix
date: 2026-03-24
---

* **MongoDBUser**: Correctly set `authSource` in the generated connection string secret to reflect `spec.db` instead of hardcoding it to `admin`.
