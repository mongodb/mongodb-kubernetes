---
kind: fix
date: 2026-06-11
---

* **MongoDBUser**: Passwords containing spaces are now correctly percent-encoded in generated connection string secrets. Previously, space characters were left unescaped, producing an invalid connection string.
