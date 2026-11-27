---
kind: feature
date: 2026-11-23
---

* **Internal Service annotations**: Added optional `internalConnectivity.annotations` to the `MongoDB` resource at three levels: top-level `spec.internalConnectivity`, per-component (`spec.shard`, `spec.configSrv`, `spec.mongos`), and per-member-cluster `clusterSpecList[]`. The operator merges these annotations onto the internal headless Services it creates — the per-pod `<pod>-svc` Services in multi-cluster topologies and the shared `<name>-svc` headless Service in single-cluster ones — with the most specific entry winning on key conflicts, and preserves annotations applied by other tools across reconciles. Per-pod Services support the same placeholder substitution as `spec.externalAccess.externalService.annotations`. Annotations containing per-pod placeholders are omitted from shared headless Services.
