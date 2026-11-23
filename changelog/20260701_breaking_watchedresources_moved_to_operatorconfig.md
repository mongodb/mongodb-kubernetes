---
kind: breaking
date: 2026-07-01
---

* **Operator**: The `operator.watchedResources` Helm value and the `-watch-resource` operator flag have been removed. Configure which custom resources the operator reconciles using `.spec.watchedResources` in the `OperatorConfig` CR instead. When omitted, it defaults to all known MCK CRDs (`mongodb`, `opsmanagers`, `mongodbusers`, `mongodbcommunity`, `mongodbsearch`, `mongodbmulticluster`, `clustermongodbroles`, `voyageais`). The previously implicit enablement of `mongodbmulticluster` (derived from the `multiCluster.clusters` Helm value) is now expressed explicitly through this list. Note that listing `clustermongodbroles` in `.spec.watchedResources` no longer has any effect: `ClusterMongoDBRole` support is an opt-in feature gated by RBAC presence rather than configuration (see the 2026-11-18 changelog entry). Users who previously restricted the watched resources must create or update their `OperatorConfig` resource before upgrading to MCK 2.0.
