# MongoDB Search with External Multi-Cluster Replica Set + Managed Envoy LB

## Overview

This scenario deploys **MongoDB Search** against a **MongoDB replica set that already exists outside the operator** — for example, on VMs, a separate Kubernetes cluster, or a self-hosted deployment — spread across two Kubernetes clusters. The operator creates a **MongoDBSearch** resource with `spec.source.external` pointing at the replica set's member hosts, and provisions one mongot StatefulSet plus one operator-managed Envoy proxy per member cluster.

The `internal_*` snippets in `code_snippets/` only simulate that external replica set for CI purposes. If you're following these steps against your own MongoDB deployment, skip every `internal_*` snippet — start from the numbered snippets that remain.

## About the Managed Envoy Load Balancer

> **Note:** Each member cluster gets its own operator-managed Envoy Deployment and proxy Service, fronting that cluster's own mongot pods. mongod processes reach mongot through this proxy rather than connecting to mongot directly.

```
mongod (cl 0)  ─┐
mongod (cl 0)  ─┤
mongod (cl 1)  ─┼─→ Envoy (cl 0) ─→ mongot (cl 0)
mongod (cl 1)  ─┘
```

**Known routing limitation:** `MongoDBMultiCluster` has no per-cluster `additionalMongodConfig` today, so every mongod member across both clusters is configured with the same `mongotHost` — cluster 0's Envoy proxy Service. The operator still provisions a managed Envoy and a mongot in cluster 1, but nothing points at cluster 1's Envoy, so cluster 1's mongods route search traffic cross-cluster to cluster 0 instead of being served locally. Scenario 13 (sharded multi-cluster) has the same per-cluster limitation, with per-shard routing granularity layered on top.

## Prerequisites

- (Required) A running MongoDB replica set whose members are reachable from both Kubernetes clusters over a service mesh (e.g. a multi-primary Istio mesh)
- (Required) Two Kubernetes clusters with `kubectl` contexts configured, one of which acts as the central/operator cluster
- (Required) `kubectl`, `helm`, and the `kubectl-mongodb` plugin (`kubectl mongodb multicluster setup`) installed
- (Required) MongoDB Search requires MongoDB **8.2** or later
- (Required) Ops Manager or Cloud Manager API credentials (URL, user, API key, org ID)
- (Conditional) cert-manager, if you don't already run a TLS certificate issuer in your clusters
- (Conditional) An image pull secret, if your clusters can't pull the operator/mongot images anonymously

## Before You Begin

### Set Up Your Environment

1. **Required:** Set environment variables. Edit `env_variables.sh` with your two cluster contexts, replica set member host:port entries, and Ops Manager/Cloud Manager credentials, then source it: `source env_variables.sh`. This file also covers namespace, resource naming, TLS secret naming, and replica-set/mongot topology sizing.
2. **Optional:** Validate environment variables. Run `code_snippets/12_0040_validate_env.sh` to confirm every required variable is set and that both Kubernetes contexts exist before you proceed.

### Set Up Kubernetes and the Operator

1. **Required:** Create namespaces. Run `code_snippets/12_0045_create_namespaces.sh` to create `MDB_NS` in both member clusters, with Istio sidecar injection enabled for cross-cluster service discovery.
2. **Conditional:** Install the operator. If the operator isn't already installed in multi-cluster mode, run `code_snippets/12_0100_install_operator.sh` — this runs `kubectl mongodb multicluster setup` to configure per-cluster service accounts and the multi-cluster kubeconfig Secret, then installs the operator via Helm on the central cluster.

### Configure TLS Certificates

1. **Conditional:** Install cert-manager. If it isn't already running, `code_snippets/12_0301_install_cert_manager.sh` installs it on the central cluster (certificates are issued there and their Secrets replicated out to the other member cluster).
2. **Required:** Configure TLS prerequisites. Run `code_snippets/12_0302_configure_tls_prerequisites.sh` to create the self-signed bootstrap issuer, the CA certificate, and the CA issuer that later certificates chain from.
3. **Required:** Generate TLS certificates. Run `code_snippets/12_0316a_create_mongot_tls_certificates.sh` to issue the shared mongot TLS certificate, then `code_snippets/12_0316b_create_lb_tls_certificates.sh` to issue the per-cluster Envoy server/client certificate pairs.
4. **Required:** Distribute certificates to member clusters. Run `code_snippets/12_0317_replicate_search_secrets.sh` to copy the mongot cert, the per-cluster LB certs, and the search sync user password from the central cluster to every other member cluster — the operator does not replicate Search-prefixed Secrets on its own.

## Configure MongoDB Search

1. **Required:** Create the MongoDBSearch resource. Run `code_snippets/12_0320_create_mongodb_search_resource.sh` to create the resource with `spec.source.external` (the replica set's seed host list) and a per-cluster `loadBalancer.managed` entry with each cluster's own `externalHostname`.
2. **Required:** Wait for it to become Running. Run `code_snippets/12_0325_wait_for_search_resource.sh`, which polls `status.phase` on the MongoDBSearch resource and then lists the mongot pods that came up in each member cluster.
3. Show running pods. Run `code_snippets/12_0330_show_running_pods.sh` to list pods, Services, and the MongoDBSearch resource across both clusters as a final sanity check.

## Next Steps

Once the MongoDBSearch resource reports `Running`, continue with the query snippets in [`../03-search-query-usage/`](../03-search-query-usage/) against your replica set to import data, create search indexes, and run search queries.
