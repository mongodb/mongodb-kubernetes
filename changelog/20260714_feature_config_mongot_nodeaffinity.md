---
kind: feature
date: 2026-07-14
---

* **MongoDBSearch**: Added support to configure the node affinity of the MongoDB Search (`mongot`) pods using the `MongoDBSearch` CRs fields `spec.clusters[].nodeAffinity` or `spec.clusters[].shardOverrides.nodeAffinity`.
