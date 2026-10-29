---
kind: feature
date: 2026-10-07
---

* **MongoDBSearch**: Added optional `spec.clusters[].service.metadata.labels` and `spec.clusters[].service.metadata.annotations` to set labels and annotations on the Services the operator creates for Search, present from the first Service creation. For external sharded sources, `spec.clusters[].shardOverrides[].service.metadata` merges per key onto the cluster values for those shards' Services; the cluster-level mongos proxy Service always uses the cluster values. The operator-managed label keys `app`, `component`, `shard`, `mongodb.com/search-name` and `mongodb.com/search-namespace` are rejected in `spec.clusters[].service` and ignored in shard overrides.
