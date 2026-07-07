# MongoDB Search with External Multi-Cluster Sharded MongoDB + Managed Envoy LB

Deploy **MongoDB Search** against your **existing multi-cluster sharded MongoDB cluster** using the operator's **managed Envoy load balancer** across two Kubernetes clusters.

## Prerequisites

**You must already have a running MongoDB sharded cluster** whose mongos routers and shard members are reachable from both Kubernetes clusters over the service mesh (e.g. a multi-primary Istio mesh). Set the host endpoints in `env_variables.sh` before running any snippet.

The `internal_*` snippets exist only to simulate that external cluster for CI purposes. If you are following these steps against your own MongoDB deployment, skip all `internal_*` snippets.

## Overview

This scenario covers the case where the MongoDB source is external to the operator — for example, running on VMs, a separate Kubernetes cluster, or a self-hosted deployment. The operator deploys a **MongoDBSearch** resource with `spec.source.external.shardedCluster`, provisions one mongot StatefulSet per (cluster, shard), and manages a per-cluster Envoy proxy (with a per-shard route) in every member cluster.

### Traffic Flow

```
mongos (cl 0 + cl 1) ───────────→ Envoy cluster-level (cl 0) ─→ cl 0 mongots (all shards)
shard 0 mongod (cl 0 + cl 1) ───→ Envoy shard 0 (cl 0) ───────→ mongot shard 0 (cl 0)
shard 1 mongod (cl 0 + cl 1) ───→ Envoy shard 1 (cl 0) ───────→ mongot shard 1 (cl 0)
shard 2 mongod (cl 0 + cl 1) ───→ Envoy shard 2 (cl 0) ───────→ mongot shard 2 (cl 0)
```

### Search routing limitation

The source `MongoDB` CR uses `topology: MultiCluster` (not the `MongoDBMultiCluster` kind, which is only used for replica sets — see scenario 12), and it has no per-cluster `additionalMongodConfig`. Shard search routing is instead set per shard via `shardOverrides[].additionalMongodConfig`. So every shard's mongods — in **both** clusters — get the same `mongotHost`: cluster 0's per-shard Envoy proxy. mongos routers likewise point at cluster 0's cluster-level proxy via `spec.mongos.additionalMongodConfig`. Each cluster's Envoy fronts only that cluster's own mongots.

The operator still provisions a per-cluster Envoy and per-(cluster, shard) mongot StatefulSets in cluster 1, but no mongod or mongos `mongotHost` targets them today, so shard mongods in cluster 1 route search traffic cross-cluster over the mesh to cluster 0. This is the same per-cluster limitation as scenario 12 (replica set); the sharded difference is per-shard routing granularity, not per-cluster locality.

## Quick Start

1. Edit `env_variables.sh` and set:
   - `K8S_CTX_0`, `K8S_CTX_1` — your two cluster contexts (cluster 0 is also the central/operator cluster)
   - `MDB_MONGOS_HOST_0`, `MDB_MONGOS_HOST_1` — your mongos router host:port entries
   - `MDB_SHARD_0_HOST_CL0`, `MDB_SHARD_0_HOST_CL1`, `MDB_SHARD_1_HOST_CL0`, `MDB_SHARD_1_HOST_CL1`, `MDB_SHARD_2_HOST_CL0`, `MDB_SHARD_2_HOST_CL1` — your per-shard per-cluster mongod host:port entries
   - Ops Manager / Cloud Manager credentials
2. Source the file: `source env_variables.sh`
3. Run each snippet under `code_snippets/` in numbered order, skipping `internal_*` steps (those only simulate the external sharded cluster for CI):
   - `13_0040_validate_env.sh` — validate required environment variables and cluster contexts
   - `13_0045_create_namespaces.sh` — create `MDB_NS` in both member clusters
   - `13_0100_install_operator.sh` — run `kubectl mongodb multicluster setup` and install the operator in multi-cluster mode
   - `13_0301_install_cert_manager.sh` — install cert-manager on the central cluster
   - `13_0302_configure_tls_prerequisites.sh` — create the self-signed bootstrap issuer, CA certificate, and CA issuer
   - `13_0316a_create_mongot_tls_certificates.sh` — issue one mongot TLS certificate per (cluster, shard)
   - `13_0316b_create_lb_tls_certificates.sh` — issue the per-cluster Envoy server/client certificate pairs
   - `13_0317_replicate_search_secrets.sh` — copy the per-(cluster, shard) mongot certs, per-cluster LB certs, and search sync user password from the central cluster to every other member cluster (the operator does not replicate these)
   - `13_0320_create_mongodb_search_resource.sh` — create the MongoDBSearch resource with `spec.source.external.shardedCluster` and per-cluster `loadBalancer.managed` (with `externalHostname` and `routerHostname`)
   - `13_0325_wait_for_search_resource.sh` — wait for the MongoDBSearch resource to reach `Running`
   - `13_0330_show_running_pods.sh` — list pods/Services across both clusters
4. After `13_0325_wait_for_search_resource.sh` reports `Running`, run the query snippets from scenario 08 (`../08-search-sharded-query-usage/`) against your sharded cluster to import data, create search indexes, and run search queries.
