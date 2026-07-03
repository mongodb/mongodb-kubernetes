---
kind: fix
date: 2026-05-28
---

* **MongoDBMulticluster**: Fixed an issue where reconciliation of an entire resource could be blocked if automatic failover was disabled and a member cluster was marked as failed. Now, the operator will reconcile healthy clusters and skip the failed clusters.
* **MongoDBMulticluster**: When automatic failover is disabled, the operator will now remove the failed clusters annotation once the clusters respond successfully to health checks a consecutive number of times, allowing for recovery without manual intervention. The number of consecutive successful health checks required to remove the failed annotation is configurable via the `MDB_MEMBER_CLUSTER_REQUIRED_HEALTHY_STREAK` environment variable, or the `multiCluster.memberClusterRequiredHealthyStreak` helm value. The default is 5.
