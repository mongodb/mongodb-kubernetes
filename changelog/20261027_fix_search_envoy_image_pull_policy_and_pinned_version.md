---
kind: fix
date: 2026-10-27
---

* **MongoDBSearch**: The operator-managed Envoy load balancer now follows the `registry.pullPolicy` Helm setting, like the `mongod` and `mongot` containers. The Envoy image is pinned to `envoyproxy/envoy:v1.37.11` instead of the floating `v1.37-latest` tag, so image updates arrive through an operator upgrade.
