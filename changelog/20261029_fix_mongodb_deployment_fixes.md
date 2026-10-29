---
kind: fix
date: 2026-10-29
---

* **MongoDB deployment fixes**: Fixed two issues affecting the `MongoDB` resource: configured Vault paths for database secrets were ignored, and hostname overrides for multi-cluster replica set pods used the wrong cluster mapping when cluster specifications were reordered.
