---
kind: fix
date: 2026-09-24
---

* **MongoDBOpsManager**: Fixed a security issue when the legacy `<name>-admin-key` admin API key Secret was resolved by CR name only, letting a same-named MongoDBOpsManager CR in a different namespace inherit another deployment's GLOBAL_ADMIN credentials.

The Operator migrates to the namespace-qualified `<namespace>-<name>-admin-key` carrying the annotation `mongodb.com/v1.opsManagerNamespace` label matching the Ops Manager namespace.

