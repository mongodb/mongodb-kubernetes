---
kind: fix
date: 2026-10-29
---

* **MongoDB (sharded clusters)**: The operator now warns in single cluster deployments that `spec.shardOverrides[].podSpec.podTemplate` is ignored. Use `spec.shardOverrides[].statefulSet.spec.template` instead.
