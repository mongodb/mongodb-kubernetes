---
kind: breaking
date: 2026-06-04
---

* Remove enablePVCResize Helm Value: Removed the `operator.enablePVCResize` Helm value. PVC resize RBAC is now always created, matching the default behaviour of the previous value.
