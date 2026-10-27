---
kind: fix
date: 2026-10-14
---

* **MongoDB (sharded clusters)**: Fixed an issue where decreasing `spec.shardCount` could leave the resource in the `Failed` phase with `cannot have more than 1 MongoDB Cluster per project`, because the removed shard was not fully removed from Ops Manager.
