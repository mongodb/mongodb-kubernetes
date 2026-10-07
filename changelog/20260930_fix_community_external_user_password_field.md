---
kind: fix
date: 2026-09-30
---

* **MongoDBCommunity**: The per user connection string secrets no longer contain an empty `password` field for `$external` users, matching the enterprise behavior.
