---
kind: feature
date: 2026-11-20
---

* **Search load shedding**: Added support for mongot load shedding to the `MongoDBSearch` managed Envoy load balancer. Overloaded mongot now returns error `489` (`SearchRequestRejectedDueToOverload`) after the configured retries instead of `402`.
