---
kind: prelude
date: 2026-07-27
---

# Multi-Cluster Support for MongoDB Search

MongoDB Search now can be installed across multiple Kubernetes clusters, extending the Search and Vector Search capabilities that reached GA in MCK 1.9.0 to highly available, multi-region topologies. 

Multi-cluster Search is supported for external replica set and sharded deployments configured with `spec.source.external`, letting you add full-text and vector search to MongoDB whose members are spread across clusters, data centers, or availability zones. The operator runs a per-cluster Envoy proxy and one `mongot` StatefulSet per shard in each member cluster, so search traffic stays local to the cluster serving it. See the [MongoDB Search deployment documentation](https://www.mongodb.com/docs/kubernetes/current/fts-vs-deployment/).


## What's New

Added support to show **cluster-level status** of Search, Managed LoadBalancer, and MetricsForwarder in the `MongoDBSearch` resource's `status.clusters` field.

Added support to **configure the node affinity** of the MongoDB Search (`mongot`) pods using the `MongoDBSearch` CR fields `spec.clusters[].nodeAffinity` or `spec.clusters[].shardOverrides[].nodeAffinity`.

Multi-cluster reconciliation no longer requires any opt-in. The internal `MDB_SEARCH_ENABLE_MULTI_CLUSTER` pre-GA feature gate has been removed; multi-cluster reconciliation is now always enabled.

The **default JVM heap size** (half of the memory request) is now capped at 30GB, following the [mongot sizing guidance](https://www.mongodb.com/docs/manual/tutorial/mongot-sizing/advanced-guidance/hardware/#jvm-heap-sizing). Heap sizes above ~30GB prevent the JVM from using compressed object pointers and degrade performance. User-provided heap flags are not affected; if more than 30GB heap is required, we recommend using `jvmFlags`.
