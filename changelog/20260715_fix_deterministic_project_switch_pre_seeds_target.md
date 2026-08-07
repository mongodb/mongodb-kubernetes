---
kind: fix
date: 2026-07-15
---

* **MongoDB**: Fixed a bug where switching a `MongoDB` ReplicaSet or ShardedCluster to a new Ops Manager project could cause the automation agent to generate a random keyfile in the empty target project, breaking internal cluster authentication.
