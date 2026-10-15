---
kind: fix
date: 2026-09-24
---

* **MongoDBOpsManager**: Fixed a security issue where the legacy `<name>-admin-key` admin API key Secret was resolved by CR name only, letting a same-named MongoDBOpsManager CR in a different namespace inherit another deployment's GLOBAL_ADMIN credentials. The Operator now migrates legacy admin key Secrets to the namespace-qualified `<namespace>-<name>-admin-key` name during reconciliation and deletes the legacy Secret. Migration fails closed for legacy Secrets that do not carry the `mongodb.com/v1.opsManagerNamespace` label matching the Ops Manager namespace: verify ownership and add this label to the legacy Secret (or create the qualified Secret with the correct label) before upgrading; do not add the label blindly if same-named CRs or Secret ownership are ambiguous. Vault-backed legacy entries are not deleted automatically (known limitation, existing behavior).
