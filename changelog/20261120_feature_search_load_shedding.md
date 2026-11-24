---
kind: feature
date: 2026-11-20
---

* **Search load shedding**: Added support for mongot load shedding to the `MongoDBSearch` managed Envoy load balancer. With mongot 1.78.0 or later, an overloaded mongot returns error `489` (`SearchRequestRejectedDueToOverload`) after the configured retries instead of `402`. Earlier mongot versions always assume 3 attempts, so they behave correctly only with the default `numRetries` of 2.
