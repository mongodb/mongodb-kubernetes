---
kind: feature
date: 2026-04-01
---

* **MongoDBSearch**: The `MongoDBSearch` CRD now supports scaling search workloads across multiple mongot instances, with built-in L7 load balancing and sharded MongoDB cluster support.

  * Added support for sharded MongoDB clusters as a source, in addition to replica sets. The operator supports both operator-managed (internal) and externally-managed sharded clusters.
  * Added support for multiple mongot replicas through `spec.replicas`, enabling horizontal scaling of search capacity. For replica sets, this field controls the total mongot pods. For sharded clusters, it controls the number of mongot pods per shard.
  * Added managed load balancer support for multi-mongot deployments through `spec.loadBalancer.managed`. The operator deploys and manages an Envoy proxy that handles gRPC stream-level balancing between mongod and mongot. For sharded clusters, each shard gets its own mongot group with independent routing through the load balancer.
  * Added unmanaged load balancer support through `spec.loadBalancer.unmanaged` for users who need full control over their proxy infrastructure.
  * Added `x509` client certificate authentication for mongot-to-mongod connections through `spec.source.x509`, as an alternative to username and password authentication.
  * Added support to pass custom JVM flags to mongot processes through `spec.jvmFlags` (for example, `-Xms`, `-Xmx`). If you do not configure heap size flags, the operator automatically sets the heap size to half of the search container's memory request.
  * Added convention-based TLS secret naming through `spec.security.tls.certsSecretPrefix`, enabling automatic per-shard TLS certificate discovery for sharded clusters. This replaces the single secret reference in `spec.security.tls.certificateKeySecretRef`, which is deprecated now.
  * Updated the default `mongodb/mongodb-search` image version to `0.64.0`. The operator uses this version if you do not specify `.spec.version` in the `MongoDBSearch` resource.
  * Updated the default resource requests for search pods to `2` CPUs and `4Gi` of memory (previously `2` CPUs and `2G`).

For configuration examples and the full API reference, see the [MongoDBSearch documentation](https://www.mongodb.com/docs/kubernetes/current/fts-vs-deployment/).
