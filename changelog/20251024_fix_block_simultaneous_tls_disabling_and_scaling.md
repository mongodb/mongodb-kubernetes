---
kind: fix
date: 2025-10-24
---

* **ReplicaSet**: Blocked disabling TLS and changing member count simultaneously. These operations must now be applied separately to prevent configuration inconsistencies.
