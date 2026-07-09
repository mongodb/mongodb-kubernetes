# MongoDB Search with External Multi-Cluster Replica Set + Managed Envoy LB

Deploy **MongoDB Search** against your **existing multi-cluster MongoDB replica set** using the operator's **managed Envoy load balancer** across two Kubernetes clusters.

## Prerequisites

**You must already have a running MongoDB replica set** whose members are reachable from both Kubernetes clusters over the service mesh (e.g. a multi-primary Istio mesh). Set the member hosts in `env_variables.sh` before running any snippet.

The `internal_*` snippets exist only to simulate that external cluster for CI purposes. If you are following these steps against your own MongoDB deployment, skip all `internal_*` snippets.

## Overview

This scenario covers the case where the MongoDB source is external to the operator — for example, running on VMs, a separate Kubernetes cluster, or a self-hosted deployment. The operator deploys a **MongoDBSearch** resource with `spec.source.external` pointing at your replica set members and provisions one mongot StatefulSet plus one managed Envoy proxy per member cluster.

### Traffic Flow

```
mongod (cl 0)  ─┐
mongod (cl 0)  ─┤
mongod (cl 1)  ─┼─→ Envoy (cl 0) ─→ mongot (cl 0)
mongod (cl 1)  ─┘
```

### Search routing limitation

`MongoDBMultiCluster` has no per-cluster `additionalMongodConfig` today, so every mongod member across all clusters gets the same `mongotHost` value — set to cluster 0's Envoy proxy Service. The operator still provisions a managed Envoy and a mongot in every member cluster (each Envoy fronts only its own cluster's mongot), but no member targets cluster 1's Envoy, so cluster 1's mongods route search traffic cross-cluster to cluster 0 rather than being served locally.

This is an expected limitation for this topology. Scenario 13 (sharded multi-cluster) has the same per-cluster limitation — all shard mongods route to cluster 0's proxies regardless of which cluster they run in — but adds per-shard Envoy routing on top of it.

## Quick Start

1. Edit `env_variables.sh` and set:
   - `K8S_CTX_0`, `K8S_CTX_1` — your two cluster contexts (cluster 0 is also the central/operator cluster)
   - `MDB_RS_HOST_0_0`, `MDB_RS_HOST_0_1`, `MDB_RS_HOST_1_0`, `MDB_RS_HOST_1_1` — your replica set member host:port entries
   - Ops Manager / Cloud Manager credentials
2. Source the file: `source env_variables.sh`
3. Run each snippet under `code_snippets/` in numbered order, skipping `internal_*` steps (those only simulate the external replica set for CI):
   - `12_0040_validate_env.sh` — validate required environment variables and cluster contexts
   - `12_0045_create_namespaces.sh` — create `MDB_NS` in both member clusters
   - `12_0100_install_operator.sh` — run `kubectl mongodb multicluster setup` and install the operator in multi-cluster mode
   - `12_0301_install_cert_manager.sh` — install cert-manager on the central cluster
   - `12_0302_configure_tls_prerequisites.sh` — create the self-signed bootstrap issuer, CA certificate, and CA issuer
   - `12_0316a_create_mongot_tls_certificates.sh` — issue the shared mongot TLS certificate
   - `12_0316b_create_lb_tls_certificates.sh` — issue the per-cluster Envoy server/client certificate pairs
   - `12_0317_replicate_search_secrets.sh` — copy the mongot cert, per-cluster LB certs, and search sync user password from the central cluster to every other member cluster (the operator does not replicate these)
   - `12_0320_create_mongodb_search_resource.sh` — create the MongoDBSearch resource with `spec.source.external` and per-cluster `loadBalancer.managed`
   - `12_0325_wait_for_search_resource.sh` — wait for the MongoDBSearch resource to reach `Running`
   - `12_0330_show_running_pods.sh` — list pods/Services across both clusters
4. After `12_0325_wait_for_search_resource.sh` reports `Running`, set the query-module variables and run scenario 03 (`../03-search-query-usage/`) to import data, create search indexes, and run search queries:

   ```bash
   export K8S_CTX="${K8S_CTX_0}"
   export MDB_CONNECTION_STRING="${MDB_USER_CONNECTION_STRING}"
   ( cd ../03-search-query-usage && ./test.sh )
   ```

## Execution notes (moved from snippets)

| Snippet | Why it matters |
|---|---|
| `12_0045_create_namespaces.sh` | Applies `istio-injection=enabled` to `MDB_NS` in each member cluster so cross-cluster service discovery/routing works for source members. |
| `12_0100_install_operator.sh` | Requires the `kubectl-mongodb` plugin. If using local kind/minikube kubeconfigs with loopback API endpoints, set `MDB_PLUGIN_KUBECONFIG` to a pod-reachable kubeconfig before `kubectl mongodb multicluster setup`. |
| `12_0300_internal_create_ops_manager_resources.sh` | Internal/CI helper: creates Ops Manager Secret and ConfigMap only on the central cluster (where the multi-cluster source resource is reconciled). |
| `12_0301_install_cert_manager.sh` | Installs cert-manager only on cluster 0 (central). Certificates are issued there. |
| `12_0302a_internal_configure_tls_prerequisites_mongod.sh` | Ensures the CA ConfigMap exists on every member cluster so mongod/tools workloads can validate TLS. |
| `12_0304_internal_generate_tls_certificates.sh` | Internal/CI helper: source replica-set certificate is issued on cluster 0 and source cert Secret replication is handled by the MongoDB controller. |
| `12_0310_internal_create_mongodb_mc_rs.sh` | Internal/CI helper: search-related mongod parameters are configured manually for the source; no per-cluster `additionalMongodConfig` exists, so `mongotHost` is shared and points to cluster 0's Envoy. |
| `12_0316_internal_create_mongodb_users.sh` | Internal/CI helper: users are created from the central cluster context. |
| `12_0316a_create_mongot_tls_certificates.sh` | Issues the mongot certificate on cluster 0; Search Secrets are not auto-replicated, so `12_0317` handles replication. |
| `12_0316b_create_lb_tls_certificates.sh` | Issues one Envoy server/client certificate pair per cluster index (`search-lb-<i>-cert`, `search-lb-<i>-client-cert`) on cluster 0. |
| `12_0317_replicate_search_secrets.sh` | Replicates Search Secrets (mongot cert, per-cluster LB certs, sync password) from cluster 0 to other member clusters; required so member-cluster mongot pods can mount TLS material. |
| `12_0320_create_mongodb_search_resource.sh` | Uses `source.external.hostAndPorts` for source members and per-cluster managed load balancer hostnames. Also pins mongot resource requests/limits to fit kind test nodes. |
| `12_0326_internal_verify_envoy_deployment.sh` | Internal/CI check: expects one mongot StatefulSet and one Envoy Deployment per member cluster. |
