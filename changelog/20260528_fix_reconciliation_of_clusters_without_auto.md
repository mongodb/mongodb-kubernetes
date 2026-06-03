---
kind: fix
date: 2026-05-28
---

* Fix reconciliation of clusters when automatic failover is disabled
  * Fixed an issue where reconciliation of an entire resource could be blocked if automatic failover was disabled and a member cluster was marked as failed. Now, the operator will reconcile healthy clusters and skip the failed clusters.
  * Additionally, the operator will now remove the failed clusters annotation once the clusters respond successfully to health checks a consecutive number of times, allowing for recovery without manual intervention.
  * Note that these improvements only apply to operator installations with automatic failover disabled.
