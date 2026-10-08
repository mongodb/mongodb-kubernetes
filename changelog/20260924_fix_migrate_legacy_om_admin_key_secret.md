---
kind: fix
date: 2026-09-24
---

* **MongoDBOpsManager**: Fixed a security issue where the legacy `<name>-admin-key` admin API key Secret was resolved by CR name only, letting a same-named MongoDBOpsManager CR in a different namespace inherit another deployment's GLOBAL_ADMIN credentials. The Operator now transparently migrates legacy admin key Secrets to the namespace-qualified `<namespace>-<name>-admin-key` name during reconciliation and deletes the legacy Secret. No action required; Vault-backed legacy entries are not deleted automatically (known limitation, existing behavior).
