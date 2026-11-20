---
kind: fix
date: 2026-10-29
---

* **MongoDB (sharded clusters)**: Fixed a bug where a `spec.shardOverrides[].podSpec` or `spec.shardOverrides[].clusterSpecList[].podSpec` without `persistence` dropped the persistence set in `spec.shardPodSpec`. Affected shards switch to that persistence after upgrading, which can fail the reconcile if their volumes cannot be changed to it. To keep the current volumes, set `persistence` on the shard override before upgrading.
