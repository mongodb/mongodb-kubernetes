---
kind: fix
date: 2026-06-11
---

* **MongoDBUser**: Passwords containing spaces or plus signs are now correctly percent-encoded in generated connection string secrets. Spaces are encoded as `%20` and plus signs as `%2B`, ensuring both the Go driver and pymongo decode credentials correctly.
