---
kind: fix
date: 2026-08-04
---

* **MongoDBOpsManager**:  `MongoDBOpsManager` resource now fails reconciliation, when the secret referenced by the field `spec.applicationDatabase.passwordSecretKeyRef` does not exist, instead of silently generating a random password.
