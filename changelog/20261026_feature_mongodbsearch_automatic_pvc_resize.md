---
kind: feature
date: 2026-10-26
---

* **MongoDBSearch**: Increasing mongot storage (`persistence`, `shardOverrides[].persistence`, or a `volumeClaimTemplates` override) now expands the existing PersistentVolumeClaims in place; mongot pods restart once. The storage class must allow volume expansion. Decreasing storage is rejected.
