---
kind: breaking
date: 2026-07-01
---

* **Operator**: The `multiCluster.memberClusterRequiredHealthyStreak` Helm value and the `MDB_MEMBER_CLUSTER_REQUIRED_HEALTHY_STREAK` environment variable have been removed. Configure the number of consecutive successful health checks required before a previously failed member cluster is considered recovered using `.spec.multiCluster.memberClusterRequiredHealthyStreak` in the `OperatorConfig` CR instead. The default value remains `5`.
