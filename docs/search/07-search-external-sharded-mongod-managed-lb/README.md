# MongoDB Search with External Sharded MongoDB + Managed Envoy LB

This guide walks you through deploying **MongoDB Search** against your **existing external MongoDB sharded cluster** (running on VMs, bare metal, or another Kubernetes cluster) using the operator's **managed Envoy load balancer**.

For testing purposes, we include scripts that simulate an external cluster inside Kubernetes. In production, you would skip the "internal" scripts and point the configuration at your real MongoDB hosts.

## For Documentation Team

Scripts with an `internal_` prefix in their filename should be **excluded** from published docs (test scaffolding only).
All remaining scripts are **user-facing** and should be included in published docs.

## Overview

### What is "Managed Envoy"?

When you set `spec.lb.mode: Managed` in your MongoDBSearch resource, the operator automatically:

1. **Deploys an Envoy proxy** - A Deployment that handles L7 (application layer) load balancing
2. **Generates routing configuration** - SNI-based routing rules for each shard
3. **Creates proxy Services** - One Kubernetes Service per shard for traffic routing
4. **Manages TLS** - Configures mTLS between mongod → Envoy → mongot

You do NOT need to write Envoy configuration, deploy Envoy yourself, or create proxy Services manually.

### Traffic Flow

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        External MongoDB Cluster                              │
│  ┌─────────┐  ┌─────────┐                                                   │
│  │ shard-0 │  │ shard-1 │  (Your external mongod instances)                 │
│  └────┬────┘  └────┬────┘                                                   │
└───────┼────────────┼────────────────────────────────────────────────────────┘
        │            │
        │ TLS (SNI-based routing)
        ▼            ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                    Kubernetes Cluster                                        │
│  ┌────────────────────────────────────────┐                                 │
│  │    Envoy Proxy (operator-managed)      │                                 │
│  │    • Listens on port 27029             │                                 │
│  │    • Routes by SNI hostname            │                                 │
│  │    • mTLS to mongot backends           │                                 │
│  └────────────────┬───────────────────────┘                                 │
│           ┌───────┴───────┐                                                 │
│           ▼               ▼                                                 │
│  ┌─────────────┐  ┌─────────────┐                                           │
│  │ mongot-0    │  │ mongot-1    │  (Search pods per shard)                  │
│  │ StatefulSet │  │ StatefulSet │                                           │
│  └─────────────┘  └─────────────┘                                           │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Why Per-Shard Routing?

In a sharded cluster, each shard has its own data. MongoDB Search deploys separate mongot instances per shard, and each shard's mongod must connect to its corresponding mongot. SNI (Server Name Indication) routing allows the Envoy proxy to inspect the TLS handshake and route traffic to the correct mongot based on the hostname.

## What You're Responsible For

| Task | Your Responsibility |
|------|---------------------|
| External MongoDB cluster | ✅ You manage your mongod instances |
| Configure mongod search params | ✅ Point mongod at Envoy proxy endpoint |
| MongoDBSearch CR | ✅ Create with `lb.mode: Managed` |
| TLS certificates | ✅ Create certs for mongot (and optionally LB) |
| Envoy deployment | ❌ Operator handles this |
| Envoy configuration | ❌ Operator generates this |
| Proxy Services | ❌ Operator creates these |
| SNI routing rules | ❌ Operator configures these |

## Prerequisites

- Kubernetes cluster with kubectl access
- Helm 3.x installed
- cert-manager installed (for TLS certificates)
- MongoDB Kubernetes Operator installed
- Access to an external MongoDB 8.2.0+ sharded cluster (or use the simulated cluster in this guide)

## Quick Start

1. **Set environment variables:**
   ```bash
   source env_variables.sh
   # Edit the file to set your actual values
   ```

2. **Run all snippets:**
   ```bash
   ./test.sh
   ```

   Or run snippets individually in order.

## Key Configuration

### MongoDBSearch CR with Managed LB

```yaml
apiVersion: mongodb.com/v1
kind: MongoDBSearch
metadata:
  name: ${MDB_SEARCH_RESOURCE_NAME}
spec:
  replicas: ${MDB_MONGOT_REPLICAS}  # mongot replicas per shard
  source:
    username: search-sync-source
    passwordSecretRef:
      name: ${MDB_SEARCH_RESOURCE_NAME}-search-sync-source-password
      key: password
    external:
      shardedCluster:
        router:
          hosts:
            - "${MDB_EXTERNAL_MONGOS_HOST}"      # your mongos router
        shards:
          - shardName: ${MDB_EXTERNAL_SHARD_0_NAME}
            hosts:
              - "${MDB_EXTERNAL_SHARD_0_HOST}"    # your shard-0 mongod
          - shardName: ${MDB_EXTERNAL_SHARD_1_NAME}
            hosts:
              - "${MDB_EXTERNAL_SHARD_1_HOST}"    # your shard-1 mongod
      tls:
        ca:
          name: ${MDB_TLS_CA_SECRET_NAME}
  security:
    tls:
      certsSecretPrefix: ${MDB_TLS_CERT_SECRET_PREFIX}
  lb:
    mode: Managed  # <-- This is the key setting!
  resourceRequirements:        # optional — defaults apply if omitted
    limits:
      cpu: "2"
      memory: 3Gi
    requests:
      cpu: "1"
      memory: 2Gi
```

**Note:** There is NO `spec.lb.endpoint` - the operator creates and exposes the endpoints automatically.

### TLS Certificate Hierarchy

cert-manager needs a 3-step bootstrap chain before it can issue certificates for mongot and Envoy:

```
Self-Signed ClusterIssuer ──signs──▶ CA Certificate ──stored-in──▶ CA ClusterIssuer ──signs──▶ all other certs
```

| cert-manager Object | Env Var | Purpose |
|---------------------|---------|---------|
| Self-Signed ClusterIssuer | `MDB_TLS_SELF_SIGNED_ISSUER` | Bootstrap-only issuer; can only sign the CA's own certificate |
| CA Certificate (`isCA: true`) | `MDB_TLS_CA_CERT_NAME` / `MDB_TLS_CA_SECRET_NAME` | The root CA; stored as a Secret in the cert-manager namespace |
| CA ClusterIssuer | `MDB_TLS_CA_ISSUER` | References the CA Secret; all mongot, LB, and mongod certs are signed by this issuer |

The `certsSecretPrefix` field in the CR (`MDB_TLS_CERT_SECRET_PREFIX`) determines how the operator locates TLS secrets. It expects secrets named `{prefix}-{resource}-search-0-{shard}-cert` for each shard's mongot pods.

### Load Balancer Certificates

The Envoy proxy terminates one mTLS session (from mongod) and initiates another (to mongot), so it needs **two** certificates:

| Certificate | Secret Name Pattern | Usages | dnsNames | Purpose |
|-------------|---------------------|--------|----------|---------|
| Server cert | `{prefix}-{name}-search-lb-cert` | `server auth`, `client auth` | Per-shard proxy Service FQDNs + wildcard | Presented to mongod during TLS handshake |
| Client cert | `{prefix}-{name}-search-lb-client-cert` | `client auth` only | Wildcard (`*.{namespace}.svc.cluster.local`) | Used by Envoy when connecting to mongot |

Both certificates must be signed by the same CA that mongod and mongot trust (i.e., the CA ClusterIssuer created above).

### Operator-Created Resources

When you apply the MongoDBSearch CR with `lb.mode: Managed`, the operator creates:

| Resource | Name Pattern | Purpose |
|----------|--------------|---------|
| ConfigMap | `{name}-search-lb-config` | Envoy bootstrap configuration |
| Deployment | `{name}-search-lb` | Envoy proxy pods |
| Service (per shard) | `{name}-search-0-{shardName}-proxy-svc` | SNI routing endpoints |
| StatefulSet (per shard) | `{name}-search-0-{shardName}` | mongot pods for one shard |
| Service (per shard, headless) | `{name}-search-0-{shardName}-svc` | Stable DNS for mongot pods |

> **Note on `search-0`:** The `0` in `search-0` is the search deployment index (currently always `0`).
> It appears in StatefulSet names, Service names, proxy Service names, and TLS secret names.
> For example, shard `shard-0` produces: StatefulSet `mySearch-search-0-shard-0`, headless Service
> `mySearch-search-0-shard-0-svc`, proxy Service `mySearch-search-0-shard-0-proxy-svc`, and TLS secret
> `{prefix}-mySearch-search-0-shard-0-cert`.

### Configuring External mongod

Your external mongod instances must be configured to connect to the operator-created proxy Services.

> **Production ordering:** Deploy the MongoDBSearch CR first and wait for it to reach Running phase.
> The operator creates proxy Services during reconciliation. Then configure your external mongod
> instances with `setParameter` pointing at those proxy endpoints. The simulated-cluster scripts in
> this guide set endpoints at creation time because Service names are deterministic, but real clusters
> should confirm that Services exist before configuring mongod.

The endpoint format is:

```
{search-name}-search-0-{shard-name}-proxy-svc.{namespace}.svc.cluster.local:27029
```

Example for shard-0:
```
${MDB_PROXY_HOST_SHARD_0}
```

Set these mongod parameters on each shard:
```javascript
{
  setParameter: {
    mongotHost: "${MDB_PROXY_HOST_SHARD_0}",
    searchIndexManagementHostAndPort: "${MDB_PROXY_HOST_SHARD_0}",
    searchTLSMode: "requireTLS",
    useGrpcForSearch: true
  }
}
```

## Troubleshooting

### Envoy Pod Not Starting

**Symptoms:** The `{name}-search-lb` Deployment has 0/1 ready pods.

**Check:**
```bash
kubectl describe deployment ${MDB_SEARCH_RESOURCE_NAME}-search-lb -n ${MDB_NS}
kubectl logs -l app=${MDB_SEARCH_RESOURCE_NAME}-search-lb -n ${MDB_NS}
```

**Common causes:**
- TLS certificate secrets not found - ensure certificates are created first
- ConfigMap not ready - check if `${MDB_SEARCH_RESOURCE_NAME}-search-lb-config` exists
- Image pull issues - check image pull secrets

### mongod Cannot Reach Envoy

**Symptoms:** Search queries fail with connection errors.

**Check:**
```bash
# Verify proxy Services exist in K8s
kubectl get svc -n ${MDB_NS} | grep proxy-svc

# From your mongod host, test connectivity to the Envoy proxy endpoint
curl -v ${MDB_PROXY_HOST_SHARD_0}
# or
openssl s_client -connect <envoy-endpoint>:27029 -servername <sni-hostname>
```

> **Note:** The external mongod host must have network connectivity to the K8s cluster's Envoy Service
> (e.g., via LoadBalancer, NodePort, or VPN).

**Common causes:**
- Proxy Services not created - MongoDBSearch may not be in Running phase
- Network policies blocking traffic
- DNS resolution issues
- External mongod host cannot reach K8s cluster network — ensure firewall/VPN allows traffic to Envoy Service

### Search Index Creation Fails

**Symptoms:** `createSearchIndex` command times out or fails.

**Check:**
```bash
# Verify mongot pods are running
kubectl get pods -n ${MDB_NS} | grep search

# Check mongot logs
kubectl logs ${MDB_SEARCH_RESOURCE_NAME}-search-0-${MDB_EXTERNAL_SHARD_0_NAME}-0 -n ${MDB_NS}
```

**Common causes:**
- mongot cannot connect to MongoDB (check source credentials)
- TLS CA mismatch between mongod and mongot
- mongot pods not ready yet

### MongoDBSearch Stuck in Pending

**Symptoms:** MongoDBSearch resource doesn't reach Running phase.

**Check:**
```bash
kubectl describe mongodbsearch ${MDB_SEARCH_RESOURCE_NAME} -n ${MDB_NS}
kubectl get events -n ${MDB_NS} --field-selector involvedObject.name=${MDB_SEARCH_RESOURCE_NAME}
```

**Common causes:**
- Missing password secret for search-sync-source user
- Invalid external cluster configuration (wrong hostnames)
- TLS certificate secrets missing

## Glossary

| Term | Definition |
|------|------------|
| **SNI** | Server Name Indication - A TLS extension that allows a client to specify the hostname it's connecting to, enabling one server to host multiple TLS certificates |
| **mTLS** | Mutual TLS - Both client and server authenticate each other using certificates |
| **L7 Load Balancer** | Application layer load balancer that can inspect HTTP/gRPC traffic and make routing decisions based on content |
| **mongot** | MongoDB Search server that indexes and serves search queries |
| **Envoy** | High-performance L7 proxy used for traffic routing |

## Files in This Directory

| File | Description |
|------|-------------|
| `env_variables.sh` | Main configuration - edit this first |
| `env_variables_e2e_private.sh` | E2E test overrides for private/enterprise testing |
| `env_variables_e2e_private_dev.sh` | E2E test overrides for dev environment |
| `env_variables_e2e_prerelease.sh` | E2E test overrides for prerelease builds |
| `env_variables_e2e_public.sh` | E2E test overrides for public/community testing |
| `test.sh` | Runner script that executes all snippets in order |
| `code_snippets/` | Individual shell scripts for each step (see below) |

### Code Snippets (Execution Order)

**Prerequisites:**

| Script | Description |
|--------|-------------|
| `07_0040_validate_env.sh` | Validate that all required environment variables are set. Checks that you configured the necessary environment variables before running the deployment. Run this first to catch configuration issues early. |
| `07_0045_create_namespaces.sh` | Create the Kubernetes namespace for MongoDB resources. |
| `07_0046_internal_create_image_pull_secrets.sh` | **[INTERNAL]** Create image pull secrets. Only required when using private container registries. |
| `07_0090_helm_add_mongodb_repo.sh` | Add the MongoDB Helm repository. |
| `07_0100_install_operator.sh` | Install the MongoDB Kubernetes Operator. |
| `07_0300_create_ops_manager_resources.sh` | Create Ops Manager or Cloud Manager connection resources (credentials Secret and project ConfigMap). |

**TLS Configuration:**

| Script | Description |
|--------|-------------|
| `07_0301_install_cert_manager.sh` | Install cert-manager for TLS certificate management. |
| `07_0302_configure_tls_prerequisites.sh` | Configure TLS prerequisites: create a self-signed ClusterIssuer, CA Certificate, and CA ClusterIssuer. |
| `07_0302a_configure_tls_prerequisites_mongod.sh` | Distribute the CA certificate for mongod by creating a ConfigMap with the CA in the target namespace. MongoDB Enterprise expects the CA in a ConfigMap (key `ca-pem`). In production, the external mongod cluster manages its own CA distribution. |
| `07_0302b_configure_tls_prerequisites_mongot.sh` | Configure the CA certificate for mongot by creating a Secret with the CA in the target namespace. MongoDBSearch expects the CA in a Secret (key `ca.crt`). |
| `07_0304_generate_tls_certificates.sh` | Generate TLS certificates for MongoDB shards, config servers, and mongos. Cluster layout: 2 shards (1 mongod each), 2 config servers, 1 mongos. |

**Simulated External Cluster:**

| Script | Description |
|--------|-------------|
| `07_0310_create_external_mongodb_sharded_cluster.sh` | Create the simulated external MongoDB sharded cluster. The cluster is created with search parameters pre-configured to point to the operator-managed Envoy proxy endpoints. Cluster layout: 2 shards (1 mongod each), 1 mongos, 2 config servers. |
| `07_0311_internal_update_coredns_configmap.sh` | **[INTERNAL]** Update CoreDNS to resolve the external domain to the mongos ClusterIP. Simulates external DNS resolution within the single-cluster test environment. |
| `07_0315_wait_for_external_cluster.sh` | Wait for the simulated external MongoDB sharded cluster to reach Running phase (up to 15 min). |
| `07_0316_create_external_mongodb_users.sh` | Create MongoDB users for the simulated external sharded cluster: admin user, application user, and search-sync-source user with the `searchCoordinator` role. |

**MongoDB Search with Managed Envoy LB:**

| Script | Description |
|--------|-------------|
| `07_0316a_create_mongot_tls_certificates.sh` | Create TLS certificates for MongoDB Search (mongot) pods. The `search-0` in names refers to the first search deployment; the operator names StatefulSets as `{resource}-search-{index}-{shard}`. Layout: 2 shards, 2 mongot replicas each. |
| `07_0316b_create_lb_tls_certificates.sh` | Create TLS certificates for the managed load balancer (Envoy proxy). Server cert handles incoming mongod connections. Client cert handles outgoing mongot connections. |
| `07_0320_create_mongodb_search_resource.sh` | Create the MongoDBSearch resource with External Sharded Source and Managed Envoy LB. Traffic flow: External mongod to Envoy (port 27029) to mongot (port 27028). Layout: 1 mongos router, 2 shards (1 mongod each), 2 mongot replicas per shard. |
| `07_0325_wait_for_search_resource.sh` | Wait for the MongoDBSearch resource to reach Running phase (up to 10 min). |

**Verification:**

| Script | Description |
|--------|-------------|
| `07_0326_verify_envoy_deployment.sh` | Verify that the operator-managed Envoy proxy is deployed and running. Checks ConfigMap, Deployment, and per-shard proxy Services. |
| `07_0330_show_running_pods.sh` | Show all running pods in the namespace: MongoDB sharded cluster pods (simulated external), mongot pods (MongoDB Search), Envoy proxy pods (operator-managed), and Operator pods. |

> **Note:** Data import, search index creation, and search query testing are in the shared
> [`08-search-sharded-query-usage`](../08-search-sharded-query-usage/) module, which is reusable
> across all sharded search scenarios.

**Cleanup:**

| Script | Description |
|--------|-------------|
| `07_9010_delete_namespace.sh` | Delete the namespace and all resources. **WARNING:** This deletes the MongoDB sharded cluster, MongoDB Search resources, Envoy proxy, and all data. This is a destructive operation. |
