---
kind: fix
date: 2026-08-04
---

* **MongoDBOpsManager**: `spec.applicationDatabase.passwordSecretKeyRef` now fails reconciliation when the referenced secret does not exist, instead of silently generating a random password.
