---
kind: fix
date: 2026-10-29
---

* **MongoDB (sharded clusters)**: Fixed a bug where a member cluster added through `spec.shardOverrides[].clusterSpecList` ignored the persistence set in `spec.shardOverrides[].podSpec`. Affected clusters switch to that persistence after upgrading, which can fail the reconcile if their volumes cannot be changed to it. To keep the current volumes, set `persistence` on the added cluster before upgrading.
