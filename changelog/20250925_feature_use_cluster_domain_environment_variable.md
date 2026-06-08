---
kind: feature
date: 2025-09-25
---

* **MongoDBCommunity**: Added support to configure custom cluster domain via newly introduced `spec.clusterDomain` resource field. If `spec.clusterDomain` is not set, environment variable `CLUSTER_DOMAIN` is used as cluster domain. If the environment variable `CLUSTER_DOMAIN` is also not set, operator falls back to `cluster.local` as default cluster domain.
