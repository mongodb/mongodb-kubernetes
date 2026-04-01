---
kind: feature
date: 2026-03-25
---

**MongoDBSearch:** Multiple mongot instances, L7 Load Balancing, and Sharded Cluster Support

The `MongoDBSearch` CRD now supports scaling search workloads across multiple mongot instances, with built-in L7 load balancing and sharded MongoDB cluster support.

- Sharded MongoDB cluster support: each shard gets its own mongot group with independent routing through the load balancer.
- Scale mongot horizontally with `spec.replicas`. For replica sets this controls the total mongot pods; for sharded clusters, the number of mongot pods per shard.
- Load balancer fully managed by the operator, via `spec.loadBalancer.managed`, handling gRPC stream-level balancing between mongod and mongot.
- Bring-Your-Own load balancer support via `spec.loadBalancer.unmanaged` for users who need full control over their proxy infrastructure.
- x509 client certificate authentication for mongot-to-mongod connections via `spec.source.x509`, as an alternative to username/password.
- Custom JVM flags for mongot via `spec.jvmFlags` (e.g., `-Xms`, `-Xmx`). The operator auto-calculates heap size when not specified.
- Convention-based TLS secret naming via `spec.security.tls.certsSecretPrefix`, replacing the single secret reference (required for sharded clusters).
- Updated the default `mongodb/mongodb-search` image version to `0.64.0`. This is the version of search that MCK uses if `.spec.version` is not specified in the `MongoDBSearch` resource.

<!-- TODO: put correct link -->
For configuration examples and the full API reference, see the [MongoDBSearch documentation](link-to-docs).
