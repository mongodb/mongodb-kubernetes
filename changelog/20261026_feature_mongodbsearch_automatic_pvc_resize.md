---
kind: feature
date: 2026-10-26
---

* **MongoDBSearch**: Increasing the mongot storage size (`spec.clusters[].persistence`, `spec.clusters[].shardOverrides[].persistence` or a `statefulSet` `volumeClaimTemplates` override) now expands the existing mongot PersistentVolumeClaims in place, the same way as for MongoDB resources: the resource reports `Pending` with the resize progress in `status.pvc`, the mongot StatefulSets are recreated with the new size, the mongot pods restart, and the resource returns to `Running` without losing its search indexes. The storage class must allow volume expansion. Decreasing the storage size is rejected with a `Failed` status.
