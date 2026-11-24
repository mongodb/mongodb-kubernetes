---
kind: fix
date: 2026-11-24
---

* **Search Envoy retries**: Fixed an issue where the `MongoDBSearch` managed Envoy load balancer allowed only 3 concurrent retries per mongot cluster, so under load some requests failed without being retried. The limit is now 1024.
