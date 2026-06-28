---
kind: feature
date: 2026-06-22
---

* **MongoDBSearch**: The `.spec.prometheus` field has moved to `.spec.observability.prometheus`. Update your `MongoDBSearch` resources to use the new path. Prometheus metrics are now enabled by default. Set `.spec.observability.prometheus.mode` to `disabled` to disable them.
