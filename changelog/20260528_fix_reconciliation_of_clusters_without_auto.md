---
kind: fix
date: 2026-05-28
---

* Fix reconciliation of clusters when automatic failover is disabled
  * Fixed an issue where reconciliation of an entire resource could be blocked if automatic failover was disabled and a member cluster was marked as failed. Now, the operator will reconcile healthy clusters and skip the failed clusters.
  * Additionally, the operator will now remove the failed clusters annotation once the clusters respond successfully to health checks a consecutive number of times, allowing for recovery without manual intervention.
  * The number of consecutive successful health checks required to remove the failed annotation is configurable via the `MDB_REQUIRED_HEALTHY_STREAK` environment variable, or the `multiCluster.requiredHealthyStreak` helm value. The default is 5.
  * Note that this behavior applies only to operator installations with automatic failover disabled.
