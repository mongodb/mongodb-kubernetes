---
kind: fix
date: 2026-10-09
---

* **Sharded cluster**: Fixed a bug where scaling down `spec.shardCount` left the hosts of removed shards registered in Ops Manager, keeping orphaned processes visible in the deployment view.
