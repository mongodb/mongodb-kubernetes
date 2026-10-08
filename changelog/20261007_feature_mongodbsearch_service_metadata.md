---
kind: feature
date: 2026-10-07
---

* **MongoDBSearch**: Configure Search Service labels and annotations with `spec.clusters[].service.metadata` to meet admission-policy metadata requirements from the first Service creation. For external sharded sources, use `spec.clusters[].shardOverrides[].service.metadata` to merge per-shard metadata onto cluster values. Operator-generated labels take precedence over user labels.
