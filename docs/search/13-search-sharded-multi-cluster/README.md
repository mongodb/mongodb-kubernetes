# MongoDB Search with External Multi-Cluster Sharded MongoDB + Managed Envoy LB

Deploy **MongoDB Search** against your **existing multi-cluster sharded MongoDB cluster** using the operator's **managed Envoy load balancer** across two Kubernetes clusters.

## Prerequisites

**You must already have a running MongoDB sharded cluster** whose mongos routers and shard members are reachable from both Kubernetes clusters over the service mesh (e.g. a multi-primary Istio mesh). Set the host endpoints in `env_variables.sh` before running any snippet.

The `internal_*` snippets exist only to simulate that external cluster for CI purposes. If you are following these steps against your own MongoDB deployment, skip all `internal_*` snippets.

## Overview

This scenario covers the case where the MongoDB source is external to the operator — for example, running on VMs, a separate Kubernetes cluster, or a self-hosted deployment. The operator deploys a **MongoDBSearch** resource with `spec.source.external.shardedCluster`, provisions one mongot StatefulSet per (cluster, shard), and manages a per-cluster Envoy proxy (with a per-shard route) in every member cluster.

### Traffic Flow

`MongoDBMultiCluster` sources have no per-cluster `additionalMongodConfig`, and shard search routing is set per shard via `shardOverrides[].additionalMongodConfig`. So every shard's mongods — in **both** clusters — get the same `mongotHost`: cluster 0's per-shard Envoy proxy. mongos routers likewise point at cluster 0's cluster-level proxy. Each cluster's Envoy fronts only that cluster's own mongots.

```
mongos (cl 0 + cl 1) ───────────→ Envoy cluster-level (cl 0) ─→ cl 0 mongots (all shards)
shard 0 mongod (cl 0 + cl 1) ───→ Envoy shard 0 (cl 0) ───────→ mongot shard 0 (cl 0)
shard 1 mongod (cl 0 + cl 1) ───→ Envoy shard 1 (cl 0) ───────→ mongot shard 1 (cl 0)
shard 2 mongod (cl 0 + cl 1) ───→ Envoy shard 2 (cl 0) ───────→ mongot shard 2 (cl 0)
```

The operator still provisions a per-cluster Envoy and per-(cluster, shard) mongot StatefulSets in cluster 1, but no mongod or mongos `mongotHost` targets them today, so shard mongods in cluster 1 route search traffic cross-cluster over the mesh to cluster 0. This is the same per-cluster limitation as scenario 12 (replica set); the sharded difference is per-shard routing granularity, not per-cluster locality.

## Quick Start

1. Edit `env_variables.sh` and set:
   - `K8S_CTX_0`, `K8S_CTX_1` — your two cluster contexts
   - `MDB_MONGOS_HOST_*` — your mongos router host:port entries
   - `MDB_SHARD_*_HOST_CL*` — your per-shard per-cluster mongod host:port entries
   - Ops Manager / Cloud Manager credentials
2. Source the file: `source env_variables.sh`
3. Run each snippet in numbered order, skipping `internal_*` steps
4. After running `13_0320_create_mongodb_search_resource.sh`, wait for `13_0325_wait_for_search_resource.sh` to report Running, then run the query snippets from scenario 08
