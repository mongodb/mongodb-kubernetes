---
kind: feature
date: 2025-09-25
---

* **MongoDBCommunity**: Add support for custom cluster domains via `CLUSTER_DOMAIN` environment variable and `spec.clusterDomain` resource field
  * Add optional `clusterDomain` field to MongoDBCommunity CRD with hostname format validation
  * Fallback hierarchy for cluster domain: `spec.clusterDomain` resource field → `CLUSTER_DOMAIN` environment variable → default `cluster.local`
