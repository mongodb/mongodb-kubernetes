---
kind: feature
date: 2026-07-27
---

# Multi-Cluster Support for MongoDB Search

MongoDB Search now can be installed across multiple Kubernetes clusters, extending the Search and Vector Search capabilities that reached GA in MCK 1.9.0 to highly available, multi-region topologies. Multi-cluster Search is supported for external replica set and sharded deployments configured with `spec.source.external`, letting you add full-text and vector search to MongoDB whose members are spread across clusters, data centers, or availability zones. The operator runs a per-cluster Envoy proxy and one `mongot` StatefulSet per shard in each member cluster, so search traffic stays local to the cluster serving it. See the [MongoDB Search deployment documentation](https://www.mongodb.com/docs/kubernetes/current/fts-vs-deployment/).
