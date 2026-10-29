---
kind: fix
date: 2026-10-29
---

* **MongoDB (sharded clusters)**: Fixed a bug where a `spec.shardOverrides[].podSpec` or `spec.shardOverrides[].clusterSpecList[].podSpec` without `persistence` dropped the persistence set in `spec.shardPodSpec`.
