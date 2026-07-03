---
kind: fix
date: 2026-06-01
---

* **MongoDB**: Fixed a bug where setting a field to `null` in `additionalMongodConfig` to remove it from a deployment did not take effect. The field would either be ignored or reappear on the next reconciliation.
