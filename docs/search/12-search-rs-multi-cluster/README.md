# MongoDB Search with External Multi-Cluster Replica Set + Managed Envoy LB

Deploy **MongoDB Search** against your **existing multi-cluster MongoDB replica set** using the operator's **managed Envoy load balancer** across two Kubernetes clusters.

## Prerequisites

**You must already have a running MongoDB replica set** whose members are reachable from both Kubernetes clusters over the service mesh (e.g. a multi-primary Istio mesh). Set the member hosts in `env_variables.sh` before running any snippet.

The `internal_*` snippets exist only to simulate that external cluster for CI purposes. If you are following these steps against your own MongoDB deployment, skip all `internal_*` snippets.

## Overview

This scenario covers the case where the MongoDB source is external to the operator — for example, running on VMs, a separate Kubernetes cluster, or a self-hosted deployment. The operator deploys a **MongoDBSearch** resource with `spec.source.external` pointing at your replica set members and provisions one mongot StatefulSet plus one managed Envoy proxy per member cluster.

### Traffic Flow

```
mongod (cluster 0)  ─┐
mongod (cluster 0)  ─┤
mongod (cluster 1)  ─┤─→ Envoy (cluster 0) ─→ mongot (cluster 0)
mongod (cluster 1)  ─┘                     ─→ mongot (cluster 1)
```

### MongoDBMultiCluster search routing limitation

`MongoDBMultiCluster` has no per-cluster `additionalMongodConfig` today, so every mongod member across all clusters gets the same `mongotHost` value — set to cluster 0's Envoy proxy Service. Mongot pods are still deployed in every member cluster, but search traffic from cluster 1's mongods crosses the mesh to cluster 0's Envoy rather than being served locally.

This is an expected limitation for this topology. Scenario 13 (sharded multi-cluster) uses per-shard Envoy proxies and does not have this constraint.

## Quick Start

1. Edit `env_variables.sh` and set:
   - `K8S_CTX_0`, `K8S_CTX_1` — your two cluster contexts
   - `MDB_RS_HOST_*` — your replica set member host:port entries
   - Ops Manager / Cloud Manager credentials
2. Source the file: `source env_variables.sh`
3. Run each snippet in numbered order, skipping `internal_*` steps
4. After running `12_0320_create_mongodb_search_resource.sh`, wait for `12_0325_wait_for_search_resource.sh` to report Running, then run the query snippets from scenario 03
