---
kind: fix
date: 2026-09-11
---

* **PVC resize security isolation**: Fixed a vulnerability where the PVC resize logic could match foreign PVCs in the same namespace using an ambiguous name-prefix regex. PVC selection now verifies that the PVC is owned by the StatefulSet (via `ownerReference.UID`), preventing cross-workload PVC modification.