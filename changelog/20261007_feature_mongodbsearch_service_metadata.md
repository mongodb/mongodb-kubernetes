---
kind: feature
date: 2026-10-07
---

* **MongoDBSearch**: Added `spec.clusters[].service.metadata.labels` and `.annotations` to set metadata on the Services the operator creates. `shardOverrides[].service` overrides them per shard. Operator-managed label keys (`app`, `component`, `shard`, `mongodb.com/search-name`, `mongodb.com/search-namespace`) are rejected.
