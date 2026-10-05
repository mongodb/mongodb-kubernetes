---
kind: breaking
date: 2026-09-30
---

* **Standalone deployments**: MongoDB standalone deployments are no longer supported. The operator will reject any `MongoDB` resource with `spec.type: Standalone`. Migrate to a one member replica set instead.
