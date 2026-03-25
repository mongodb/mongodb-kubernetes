---
kind: feature
date: 2026-03-18
---

* **MongoDBSearch improvements**:
  * Updated the default `mongodb/mongodb-search` image version to 0.60.1.
  * Added support to pass JVM flags to mongot processes using `spec.jvmFlags`.
  * Updated the default resource requests for search pods to 2 CPUs and 4Gi of memory.
  * Added support for sharded MongoDB clusters (internal and external sources).
  * Added managed (Envoy) and unmanaged (BYO) load balancer support for distributing search traffic across multiple mongot replicas.
  * Added support for multiple mongot replicas per shard.
  * Added configurable Envoy proxy log level via `spec.loadBalancer.managed.logLevel`.
