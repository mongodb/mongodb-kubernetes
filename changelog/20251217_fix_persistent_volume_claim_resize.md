---
kind: fix
date: 2025-12-17
---

* **Persistent Volume Claim resize fix**: Fixed an issue where the Operator ignored namespaces when listing PVCs, causing conflicts with resizing PVCs of the same name. Now, PVCs are filtered by both name and namespace for accurate resizing.
