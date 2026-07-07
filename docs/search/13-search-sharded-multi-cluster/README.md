# MongoDB Search with External Multi-Cluster Sharded MongoDB + Managed Envoy LB

## Overview

This scenario deploys **MongoDB Search** against a **sharded MongoDB cluster that already exists outside the operator** — for example, on VMs, a separate Kubernetes cluster, or a self-hosted deployment — with its mongos routers and shard members spread across two Kubernetes clusters. The operator creates a **MongoDBSearch** resource with `spec.source.external.shardedCluster`, provisions one mongot StatefulSet per (cluster, shard), and manages a per-cluster Envoy proxy — with a per-shard route plus a router-level route — in every member cluster.

The `internal_*` snippets in `code_snippets/` only simulate that external sharded cluster for CI purposes. If you're following these steps against your own MongoDB deployment, skip every `internal_*` snippet — start from the numbered snippets that remain.

## About the Managed Envoy Load Balancer

> **Note:** Each member cluster gets its own operator-managed Envoy Deployment, with one route per shard plus one cluster-level route for mongos traffic. Every route fronts only that same cluster's own mongot pods.

```
mongos (cl 0 + cl 1) ───────────→ Envoy cluster-level (cl 0) ─→ cl 0 mongots (all shards)
shard 0 mongod (cl 0 + cl 1) ───→ Envoy shard 0 (cl 0) ───────→ mongot shard 0 (cl 0)
shard 1 mongod (cl 0 + cl 1) ───→ Envoy shard 1 (cl 0) ───────→ mongot shard 1 (cl 0)
shard 2 mongod (cl 0 + cl 1) ───→ Envoy shard 2 (cl 0) ───────→ mongot shard 2 (cl 0)
```

**Known routing limitation:** the source `MongoDB` CR uses `topology: MultiCluster` (not the `MongoDBMultiCluster` kind used for replica sets in scenario 12), and it has no per-cluster `additionalMongodConfig`. Shard routing is instead set per shard via `shardOverrides[].additionalMongodConfig`, so every shard's mongods — in **both** clusters — get the same `mongotHost`: cluster 0's per-shard Envoy proxy. mongos routers likewise point at cluster 0's cluster-level proxy via `spec.mongos.additionalMongodConfig`. The operator still provisions a per-cluster Envoy and per-(cluster, shard) mongot StatefulSets in cluster 1, but no mongod or mongos targets them today, so cluster 1 always routes search traffic cross-cluster to cluster 0. This is the same per-cluster limitation as scenario 12 (replica set); the difference here is per-shard routing granularity, not per-cluster locality.

## Prerequisites

- (Required) A running MongoDB sharded cluster whose mongos routers and shard members are reachable from both Kubernetes clusters over a service mesh (e.g. a multi-primary Istio mesh)
- (Required) Two Kubernetes clusters with `kubectl` contexts configured, one of which acts as the central/operator cluster
- (Required) `kubectl`, `helm`, and the `kubectl-mongodb` plugin (`kubectl mongodb multicluster setup`) installed
- (Required) MongoDB Search requires MongoDB **8.2** or later
- (Required) Ops Manager or Cloud Manager API credentials (URL, user, API key, org ID)
- (Conditional) cert-manager, if you don't already run a TLS certificate issuer in your clusters
- (Conditional) An image pull secret, if your clusters can't pull the operator/mongot images anonymously

## Before You Begin

### Set Up Your Environment

1. **Required:** Set environment variables. Edit `env_variables.sh` with your two cluster contexts, mongos router host:port entries, per-shard per-cluster mongod host:port entries, and Ops Manager/Cloud Manager credentials, then source it: `source env_variables.sh`. This file also covers namespace, resource naming, TLS secret naming, and shard/mongos/config-server topology sizing.
2. **Optional:** Validate environment variables. Run `code_snippets/13_0040_validate_env.sh` to confirm every required variable is set and that both Kubernetes contexts exist before you proceed.

### Set Up Kubernetes and the Operator

1. **Required:** Create namespaces. Run `code_snippets/13_0045_create_namespaces.sh` to create `MDB_NS` in both member clusters, with Istio sidecar injection enabled for cross-cluster service discovery.
2. **Conditional:** Install the operator. If the operator isn't already installed in multi-cluster mode, run `code_snippets/13_0100_install_operator.sh` — this runs `kubectl mongodb multicluster setup` to configure per-cluster service accounts and the multi-cluster kubeconfig Secret, then installs the operator via Helm on the central cluster.

### Configure TLS Certificates

1. **Conditional:** Install cert-manager. If it isn't already running, `code_snippets/13_0301_install_cert_manager.sh` installs it on the central cluster (certificates are issued there and their Secrets replicated out to the other member cluster).
2. **Required:** Configure TLS prerequisites. Run `code_snippets/13_0302_configure_tls_prerequisites.sh` to create the self-signed bootstrap issuer, the CA certificate, and the CA issuer that later certificates chain from.
3. **Required:** Generate TLS certificates. Run `code_snippets/13_0316a_create_mongot_tls_certificates.sh` to issue one mongot TLS certificate per (cluster, shard), then `code_snippets/13_0316b_create_lb_tls_certificates.sh` to issue the per-cluster Envoy server/client certificate pairs.
4. **Required:** Distribute certificates to member clusters. Run `code_snippets/13_0317_replicate_search_secrets.sh` to copy the per-(cluster, shard) mongot certs, the per-cluster LB certs, and the search sync user password from the central cluster to every other member cluster — the operator does not replicate Search-prefixed Secrets on its own.

## Configure MongoDB Search

1. **Required:** Create the MongoDBSearch resource. Run `code_snippets/13_0320_create_mongodb_search_resource.sh` to create the resource with `spec.source.external.shardedCluster` (mongos and per-shard host lists) and a per-cluster `loadBalancer.managed` entry, each with its own `externalHostname` (per-shard, via the `{shardName}` placeholder) and `routerHostname` (shard-agnostic, for mongos).
2. **Required:** Wait for it to become Running. Run `code_snippets/13_0325_wait_for_search_resource.sh`, which polls `status.phase` on the MongoDBSearch resource and then lists the mongot pods that came up in each member cluster.
3. Show running pods. Run `code_snippets/13_0330_show_running_pods.sh` to list pods, Services, and the MongoDBSearch resource across both clusters as a final sanity check.

## Next Steps

Once the MongoDBSearch resource reports `Running`, continue with the query snippets in [`../08-search-sharded-query-usage/`](../08-search-sharded-query-usage/) against your sharded cluster to import data, create search indexes, and run search queries.
