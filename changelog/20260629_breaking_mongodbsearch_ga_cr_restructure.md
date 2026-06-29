---
kind: breaking
date: 2026-06-29
---

* **MongoDBSearch** is now generally available (GA), which significantly restructures the `MongoDBSearch` custom resource. The following schema changes are breaking: existing `MongoDBSearch` resources written for previous operator versions must be migrated before upgrading.
  * The deployment topology is now configured under `spec.clusters` instead of directly on `spec`. Move your configuration into a single `spec.clusters` entry, identified by `spec.clusters[].name`.
  * The sizing and configuration fields `spec.replicas`, `spec.statefulSet`, `spec.persistence`, `spec.resourceRequirements`, `spec.loadBalancer`, and `spec.jvmFlags` moved from the top level of `spec` to their per-entry equivalents under `spec.clusters` (for example `spec.clusters[].replicas` and `spec.clusters[].statefulSet`).
  * The `spec.prometheus` field moved to `spec.observability.prometheus`. Update your `MongoDBSearch` resources to use the new path. Prometheus metrics are now enabled by default; set `spec.observability.prometheus.mode` to `disabled` to turn them off. Previously the metrics endpoint was disabled unless `spec.prometheus` was set.
  * MongoDB Search `1.70.1` is now the minimum required version. Set `spec.version` to `1.70.1` or newer; older versions are no longer supported by the operator.
