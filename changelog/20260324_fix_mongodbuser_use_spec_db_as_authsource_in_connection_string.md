---
kind: fix
date: 2026-03-24
---

* **MongoDBUser**: The `authSource` parameter in generated connection string secrets now correctly reflects `spec.db` instead of always being set to `admin`. This affects all authentication modes, including SCRAM-SHA-256, SCRAM-SHA-1, and X.509 (where `authSource=$external` is now correctly set).