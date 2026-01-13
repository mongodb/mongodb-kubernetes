---
kind: fix
date: 2025-10-01
---

* **MongoDB Kubernetes Operator**: Operator crashed when `securityContext.readOnlyRootFilesystem=true` was set, because it was trying to create `/tmp/k8s-webhook-server` directory that was unmounted.
