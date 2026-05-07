---
kind: fix
date: 2026-04-29
---

* **MongoDBOpsManager**, **AppDB**: In multi-cluster AppDB topology, fixed a bug where the per-cluster `spec.applicationDatabase.clusterSpecList[i].statefulSet` override was silently ignored.
