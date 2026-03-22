# MongoDB Search with External Sharded MongoDB + Managed Envoy LB

This guide walks you through deploying **MongoDB Search** against your **existing external MongoDB sharded cluster** (running on VMs, bare metal, or another Kubernetes cluster) using the operator's **managed Envoy load balancer**.

For testing purposes, we include scripts that simulate an external cluster inside Kubernetes. In production, you would skip the "internal" scripts and point the configuration at your real MongoDB hosts.

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

## Getting Started

```bash
cd docs/search/07-search-external-sharded-mongod-managed-lb

# Edit env_variables.sh to set your Kubernetes context, namespace, cluster topology, and TLS settings
vi env_variables.sh

# Source the environment variables
source env_variables.sh
```

To run all steps automatically:

```bash
./test.sh
```

Or follow the steps below to run each snippet individually.

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

## Step-by-Step Execution

Run these steps in order from the `07-search-external-sharded-mongod-managed-lb` directory after sourcing `env_variables.sh`.

### Set Up Kubernetes and the Operator

#### Step 1: Validate Environment Variables

Checks that all required environment variables are set before deployment. Run this first to catch configuration issues early.

```bash
./code_snippets/07_0040_validate_env.sh
```

#### Step 2: Create Kubernetes Namespace

Create the Kubernetes namespace for MongoDB resources.

```bash
./code_snippets/07_0045_create_namespaces.sh
```

#### Step 3: Create Image Pull Secrets

> **Test-only:** This script creates image pull secrets for private test registries. In production, create pull secrets only if you use a private container registry: `kubectl create secret docker-registry ...`

```bash
./code_snippets/07_0046_internal_create_image_pull_secrets.sh
```

#### Step 4: Add MongoDB Helm Repository

```bash
./code_snippets/07_0090_helm_add_mongodb_repo.sh
```

#### Step 5: Install the MongoDB Kubernetes Operator

```bash
./code_snippets/07_0100_install_operator.sh
```

#### Step 6: Create Ops Manager Resources

> **Test-only:** This script creates Ops Manager project ConfigMap and credentials Secret using test API keys. In production, create these resources manually with your own Ops Manager or Cloud Manager credentials.

```bash
./code_snippets/07_0300_internal_create_ops_manager_resources.sh
```

### Configure TLS

#### Step 7: Install cert-manager

Install cert-manager for automated TLS certificate management.

```bash
./code_snippets/07_0301_install_cert_manager.sh
```

#### Step 8: Configure TLS Prerequisites

Create the self-signed ClusterIssuer, CA Certificate, and CA ClusterIssuer bootstrap chain.

```bash
./code_snippets/07_0302_configure_tls_prerequisites.sh
```

#### Step 9: Distribute CA Certificate for mongod

> **Test-only:** This distributes the CA certificate as a ConfigMap for the simulated in-K8s mongod cluster. In production, your external mongod cluster manages its own CA distribution.

```bash
./code_snippets/07_0302a_internal_configure_tls_prerequisites_mongod.sh
```

#### Step 10: Distribute CA Certificate for mongot

Create a Secret with the CA in the target namespace. MongoDBSearch expects the CA in a Secret (key `ca.crt`).

```bash
./code_snippets/07_0302b_configure_tls_prerequisites_mongot.sh
```

#### Step 11: Generate TLS Certificates for MongoDB

> **Test-only:** This generates TLS certificates for the simulated mongod shards, config servers, and mongos. In production, your external MongoDB cluster already has its own TLS certificates.

```bash
./code_snippets/07_0304_internal_generate_tls_certificates.sh
```

### Deploy the Simulated External Cluster

> **Production users:** Skip steps 12–15. You already have a running external MongoDB sharded cluster. Instead, create the `search-sync-source` user on your external cluster and create the password Secret in Kubernetes:
> ```bash
> kubectl create secret generic ${MDB_SEARCH_RESOURCE_NAME}-search-sync-source-password \
>   --from-literal=password=<your-password> -n ${MDB_NS}
> ```

#### Step 12: Create External MongoDB Sharded Cluster

> **Test-only:** Deploy a simulated external MongoDB sharded cluster inside Kubernetes for testing. The cluster is created with search parameters pre-configured to point to the operator-managed Envoy proxy endpoints.

```bash
./code_snippets/07_0310_internal_create_external_mongodb_sharded_cluster.sh
```

#### Step 13: Update CoreDNS

> **Test-only:** Update CoreDNS to resolve the simulated external domain within the cluster.

```bash
./code_snippets/07_0311_internal_update_coredns_configmap.sh
```

#### Step 14: Wait for External Cluster

> **Test-only:** Wait for the simulated cluster to reach Running phase (up to 15 min).

```bash
./code_snippets/07_0315_internal_wait_for_external_cluster.sh
```

#### Step 15: Create MongoDB Users

> **Test-only:** Create admin, application, and search-sync-source users on the simulated cluster.

```bash
./code_snippets/07_0316_internal_create_external_mongodb_users.sh
```

### Deploy MongoDB Search with Managed Envoy LB

#### Step 16: Create mongot TLS Certificates

Create TLS certificates for mongot pods (one cert-manager Certificate per shard).

```bash
./code_snippets/07_0316a_create_mongot_tls_certificates.sh
```

#### Step 17: Create Load Balancer TLS Certificates

Create server and client TLS certificates for the Envoy proxy. The server cert handles incoming mongod connections; the client cert handles outgoing mongot connections.

```bash
./code_snippets/07_0316b_create_lb_tls_certificates.sh
```

#### Step 18: Create MongoDBSearch Resource

Apply the MongoDBSearch CR with `lb.mode: Managed` and external cluster source configuration.

```bash
./code_snippets/07_0320_create_mongodb_search_resource.sh
```

#### Step 19: Wait for MongoDBSearch

Wait for the MongoDBSearch resource to reach Running phase (up to 10 min).

```bash
./code_snippets/07_0325_wait_for_search_resource.sh
```

### Verify the Deployment

#### Step 20: Verify Envoy Deployment

Verify that the operator-managed Envoy proxy is deployed and running. Checks ConfigMap, Deployment, and per-shard proxy Services.

```bash
./code_snippets/07_0326_verify_envoy_deployment.sh
```

#### Step 21: Show Running Pods

Show all running pods: MongoDB sharded cluster (simulated external), mongot (MongoDB Search), Envoy proxy, and Operator pods.

```bash
./code_snippets/07_0330_show_running_pods.sh
```

### Next: Import Data and Run Search Queries

Proceed to [`08-search-sharded-query-usage`](../08-search-sharded-query-usage/) to import data, create search indexes, and run search queries. That module is shared across all sharded search scenarios.

### Cleanup (Manual Only)

> **WARNING:** This deletes the namespace and all resources including the MongoDB sharded cluster, MongoDB Search resources, Envoy proxy, and all data.

```bash
./code_snippets/07_9010_internal_delete_namespace.sh
```
