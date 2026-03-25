---
kind: feature
date: 2026-03-25
---

* MongoDBSearch: Multiple mongot instances, L7 Load Balancing, and Sharded Cluster Support

The `MongoDBSearch` CRD now supports scaling search workloads across multiple mongot instances, with built-in L7 load balancing and sharded MongoDB cluster support.

- Scale mongot horizontally with `spec.replicas`. For replica sets this controls the total mongot pods; for sharded clusters, the number of mongot pods per shard.
- Operator-managed Envoy L7 load balancer via `spec.loadBalancer.managed`, handling gRPC stream-level balancing and cursor pinning between mongod and mongot.
- Bring Your Own load balancer support via `spec.loadBalancer.unmanaged` for users who need full control over their proxy infrastructure.
- Sharded MongoDB cluster support: each shard gets its own mongot group with independent routing through the load balancer via SNI-based filter chains.
- Load balancer status reporting visible via `kubectl get mdbs`.

<!-- TODO: put correct link -->
For configuration examples and the full API reference, see the [MongoDBSearch documentation](link-to-docs).
