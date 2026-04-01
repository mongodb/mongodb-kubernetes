---
kind: feature
date: 2026-04-01
---

* **MongoDBSearch** (Public Preview): The `MongoDBSearch` resource now supports horizontal scaling, L7 load balancing, and sharded MongoDB cluster support — significantly expanding the capabilities of full-text search and vector search on Enterprise Advanced.

  **Sharded cluster support**
  * The `MongoDBSearch` resource now supports sharded MongoDB clusters as a source, in addition to replica sets. The operator deploys a dedicated mongot group per shard and manages routing independently for each one. Both operator-managed and externally-managed sharded clusters are supported.

  **Horizontal scaling and load balancing**
  * Search workloads can now scale horizontally with multiple mongot replicas through `spec.replicas`. For replica sets, this controls the total mongot pods. For sharded clusters, it controls the number of mongot pods per shard.
  * Multi-mongot deployments require L7 load balancing. The operator can deploy and manage an Envoy proxy (`spec.loadBalancer.managed`) that handles gRPC stream-level balancing between mongod and mongot. Alternatively, `spec.loadBalancer.unmanaged` lets you bring your own proxy infrastructure.

  **Security and configuration**
  * Added `x509` client certificate authentication for mongot-to-mongod connections through `spec.source.x509`, as an alternative to username and password authentication.
  * Added convention-based TLS secret naming through `spec.security.tls.certsSecretPrefix`, enabling automatic per-shard TLS certificate discovery. We recommend using `certsSecretPrefix` for new deployments.
  * Added support for custom JVM flags through `spec.jvmFlags` (for example, `-Xms`, `-Xmx`). If heap size flags are not configured, the operator automatically sets the heap size to half of the container's memory request.
  * Updated the default `mongodb/mongodb-search` image version to `0.64.0`.
  * Updated the default resource requests for search pods to `2` CPUs and `4Gi` of memory (previously `2` CPUs and `2G`).

  For configuration examples and the full API reference, see the [MongoDBSearch documentation](https://www.mongodb.com/docs/kubernetes/current/fts-vs-deployment/).
