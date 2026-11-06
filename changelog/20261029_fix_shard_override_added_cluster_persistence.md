---
kind: fix
date: 2026-10-29
---

* **MongoDB (sharded clusters)**: Fixed a bug where a member cluster added through `spec.shardOverrides[].clusterSpecList` ignored the persistence set in `spec.shardOverrides[].podSpec`.
