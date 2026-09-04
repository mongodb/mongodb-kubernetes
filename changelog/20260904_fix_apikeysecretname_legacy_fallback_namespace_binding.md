---
kind: fix
date: 2026-09-04
---

* **MongoDBOpsManager**: Fixed a security issue where the legacy `<name>-admin-key` API key `Secret` fallback in `APIKeySecretName` resolved by CR name only, without binding to the owning namespace. A `MongoDBOpsManager` CR in a different namespace could inherit another CR's admin credentials by sharing the same name. The fallback now requires a `mongodb.com/v1.opsManagerNamespace` label on the `Secret` matching the CR's namespace. **Note:** deployments with a legacy-format admin key `Secret` that lacks this label must either delete the `Secret` (the operator will recreate it) or manually add the label before upgrading.
