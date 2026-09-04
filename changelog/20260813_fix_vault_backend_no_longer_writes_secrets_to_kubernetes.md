---
kind: fix
date: 2026-08-13
---

* Fixed `PutSecretIfChanged` falling through to a Kubernetes write when the Vault secret backend is enabled and the Vault copy is already up to date.
