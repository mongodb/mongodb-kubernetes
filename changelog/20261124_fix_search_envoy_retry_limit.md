---
kind: fix
date: 2026-11-24
---

* **Search Envoy retries**: Fixed an issue where the `MongoDBSearch` managed Envoy load balancer allowed only 3 concurrent retries per mongot cluster, so under load some requests failed without being retried. Retries are now limited by a retry budget: 20% of active requests, but never fewer than 1024. Set the budget and the connection and request limits with `spec.clusters[].loadBalancer.managed.circuitBreaker`.
