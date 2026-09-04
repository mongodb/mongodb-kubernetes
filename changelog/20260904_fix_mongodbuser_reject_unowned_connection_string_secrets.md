---
kind: fix
date: 2026-09-04
---

* **MongoDBUser**: Fixed a bug where the connection string `Secret` ownership guard accepted a pre-existing `Secret` that had no controller owner reference, allowing the operator to silently overwrite and adopt an administrator-created `Secret`. The guard now rejects any pre-existing `Secret` not already owned by the reconciling `MongoDBUser`.
