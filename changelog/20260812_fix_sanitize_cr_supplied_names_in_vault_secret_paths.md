---
kind: fix
date: 2026-08-12
---

* Validate CR-supplied namespace and secret names when building Vault secret paths on Vault-backed deployments, preventing path traversal that could read or overwrite secrets belonging to another namespace.
