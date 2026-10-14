---
kind: fix
date: 2026-10-14
---

* **MongoDB (sharded clusters)**: Fixed an issue where decreasing `spec.shardCount` could leave the resource in the `Failed` phase with `cannot have more than 1 MongoDB Cluster per project`, with the removed shard's replica set and processes still present in Ops Manager. The Operator now records the shards it removed and finishes their removal even when Ops Manager has already cleared them from its draining list.
