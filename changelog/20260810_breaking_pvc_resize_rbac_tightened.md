---
kind: breaking
date: 2026-08-10
---

* **PVC-resize RBAC tightened**: Removed the unused `get`, `patch`, and `delete` verbs from the Operator's PVC-resize Role or ClusterRole (`<operator-name>-pvc-resize`). The Operator only ever lists PVCs and updates their requested storage during a PVC resize, so the role now grants only `list`, `watch`, and `update` on `persistentvolumeclaims`. No action is required and PVC resizing is unaffected; this is a least-privilege tightening of the installed RBAC.
