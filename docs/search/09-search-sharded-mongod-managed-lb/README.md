# MongoDB Search with Operator-Managed Sharded MongoDB + Managed Envoy LB

This guide walks you through deploying **MongoDB Search** against an **operator-managed MongoDB sharded cluster** (deployed via the MongoDB Enterprise Kubernetes Operator) using the operator's **managed Envoy load balancer**.

Unlike [scenario 07](../07-search-external-sharded-mongod-managed-lb/) (external cluster), this scenario uses `spec.source.mongodbResourceRef` so the operator automatically configures mongod search parameters — no `shardOverrides` or manual proxy endpoint configuration needed.

## For Documentation Team

The following scripts are marked `# AUDIENCE: internal` and should be **excluded** from published docs (test scaffolding only):

| Script | Description |
|--------|-------------|
| `09_0046_create_image_pull_secrets.sh` | Create image pull secrets (private registry, CI only) |
| `09_0101_helm_install_staging_operator.sh` | Install staging/dev operator |
| `09_9010_delete_namespace.sh` | Delete namespace and all resources (cleanup/teardown) |

All remaining scripts are **user-facing** and should be included in published docs.

## Overview

### What is "Managed Envoy"?

When you set `spec.lb.mode: Managed` in your MongoDBSearch resource, the operator automatically:

1. **Deploys an Envoy proxy** - A Deployment that handles L7 (application layer) load balancing
2. **Generates routing configuration** - SNI-based routing rules for each shard
3. **Creates proxy Services** - One Kubernetes Service per shard for traffic routing
4. **Manages TLS** - Configures mTLS between mongod → Envoy → mongot
5. **Configures mongod** - Automatically sets search parameters on each shard (no `shardOverrides` needed)

You do NOT need to write Envoy configuration, deploy Envoy yourself, create proxy Services manually, or configure mongod search parameters.

### Traffic Flow

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                    Kubernetes Cluster                                        │
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐    │
│  │           Operator-Managed MongoDB Sharded Cluster                  │    │
│  │  ┌─────────┐  ┌─────────┐                                          │    │
│  │  │ shard-0 │  │ shard-1 │  (mongod managed by operator)            │    │
│  │  └────┬────┘  └────┬────┘                                          │    │
│  └───────┼────────────┼───────────────────────────────────────────────┘    │
│          │            │                                                     │
│          │ TLS (SNI-based routing)                                          │
│          ▼            ▼                                                     │
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
| MongoDB sharded cluster CR | ✅ Create the MongoDB CR (operator manages it) |
| MongoDBSearch CR | ✅ Create with `lb.mode: Managed` and `mongodbResourceRef` |
| TLS certificates | ✅ Create certs for mongot and LB |
| Configure mongod search params | ❌ Operator handles this automatically |
| Envoy deployment | ❌ Operator handles this |
| Envoy configuration | ❌ Operator generates this |
| Proxy Services | ❌ Operator creates these |
| SNI routing rules | ❌ Operator configures these |

## Prerequisites

- Kubernetes cluster with kubectl access
- Helm 3.x installed
- cert-manager installed (for TLS certificates)
- MongoDB Kubernetes Operator installed
- Ops Manager or Cloud Manager access

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

### MongoDBSearch CR with Managed LB (Operator-Managed Source)

```yaml
apiVersion: mongodb.com/v1
kind: MongoDBSearch
metadata:
  name: ${MDB_RESOURCE_NAME}
spec:
  replicas: ${MDB_MONGOT_REPLICAS}  # mongot replicas per shard
  source:
    mongodbResourceRef:
      name: ${MDB_RESOURCE_NAME}      # references the MongoDB CR directly
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

**Key difference from external scenario:** No `source.username`, `source.passwordSecretRef`, or `source.external` block — the operator infers everything from the referenced MongoDB CR.

### MongoDB CR (No Search Parameters Needed)

```yaml
apiVersion: mongodb.com/v1
kind: MongoDB
metadata:
  name: ${MDB_RESOURCE_NAME}
spec:
  type: ShardedCluster
  shardCount: 2
  mongodsPerShardCount: 1
  mongosCount: 1
  configServerCount: 2
  # No shardOverrides or mongos.additionalMongodConfig for search!
  # The operator automatically configures search parameters when MongoDBSearch is deployed.
```

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
> For example, shard `mdb-sh-0` produces: StatefulSet `mdb-sh-search-0-mdb-sh-0`, headless Service
> `mdb-sh-search-0-mdb-sh-0-svc`, proxy Service `mdb-sh-search-0-mdb-sh-0-proxy-svc`, and TLS secret
> `certs-mdb-sh-search-0-mdb-sh-0-cert`.

## Troubleshooting

### Envoy Pod Not Starting

**Symptoms:** The `{name}-search-lb` Deployment has 0/1 ready pods.

**Check:**
```bash
kubectl describe deployment ${MDB_RESOURCE_NAME}-search-lb -n ${MDB_NS}
kubectl logs -l app=${MDB_RESOURCE_NAME}-search-lb -n ${MDB_NS}
```

**Common causes:**
- TLS certificate secrets not found - ensure certificates are created first
- ConfigMap not ready - check if `${MDB_RESOURCE_NAME}-search-lb-config` exists
- Image pull issues - check image pull secrets

### Search Index Creation Fails

**Symptoms:** `createSearchIndex` command times out or fails.

**Check:**
```bash
# Verify mongot pods are running
kubectl get pods -n ${MDB_NS} | grep search

# Check mongot logs
kubectl logs ${MDB_RESOURCE_NAME}-search-0-${MDB_SHARD_0_NAME}-0 -n ${MDB_NS}
```

**Common causes:**
- mongot cannot connect to MongoDB (check source credentials)
- TLS CA mismatch between mongod and mongot
- mongot pods not ready yet

### MongoDBSearch Stuck in Pending

**Symptoms:** MongoDBSearch resource doesn't reach Running phase.

**Check:**
```bash
kubectl describe mongodbsearch ${MDB_RESOURCE_NAME} -n ${MDB_NS}
kubectl get events -n ${MDB_NS} --field-selector involvedObject.name=${MDB_RESOURCE_NAME}
```

**Common causes:**
- Referenced MongoDB CR not in Running phase
- TLS certificate secrets missing
- Operator version too old (needs search support)

## Glossary

| Term | Definition |
|------|------------|
| **SNI** | Server Name Indication - A TLS extension that allows a client to specify the hostname it's connecting to, enabling one server to host multiple TLS certificates |
| **mTLS** | Mutual TLS - Both client and server authenticate each other using certificates |
| **L7 Load Balancer** | Application layer load balancer that can inspect HTTP/gRPC traffic and make routing decisions based on content |
| **mongot** | MongoDB Search server that indexes and serves search queries |
| **Envoy** | High-performance L7 proxy used for traffic routing |
| **mongodbResourceRef** | A reference in the MongoDBSearch CR that points to an operator-managed MongoDB CR, allowing the operator to automatically configure search parameters |

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
| `09_0040_validate_env.sh` | Validate required environment variables |
| `09_0045_create_namespaces.sh` | Create Kubernetes namespace |
| `09_0046_create_image_pull_secrets.sh` | Create image pull secrets (if needed) |
| `09_0090_helm_add_mongodb_repo.sh` | Add MongoDB Helm repository |
| `09_0100_install_operator.sh` | Install MongoDB Kubernetes Operator |
| `09_0300_create_ops_manager_resources.sh` | Create Ops Manager / Cloud Manager resources |

**TLS Configuration:**
| Script | Description |
|--------|-------------|
| `09_0301_install_cert_manager.sh` | Install cert-manager |
| `09_0302_configure_tls_prerequisites.sh` | Create self-signed CA and ClusterIssuer |
| `09_0302a_configure_tls_prerequisites_mongod.sh` | Distribute CA ConfigMap for mongod |
| `09_0302b_configure_tls_prerequisites_mongot.sh` | Distribute CA Secret for mongot |
| `09_0304_generate_tls_certificates.sh` | Generate certs for shards, config servers, mongos |

**Operator-Managed MongoDB Cluster:**
| Script | Description |
|--------|-------------|
| `09_0310_create_mongodb_sharded_cluster.sh` | Create operator-managed sharded cluster |
| `09_0315_wait_for_sharded_cluster.sh` | Wait for cluster to reach Running phase |
| `09_0316_create_mongodb_users.sh` | Create MongoDB users |

**MongoDB Search with Managed Envoy LB:**
| Script | Description |
|--------|-------------|
| `09_0316a_create_mongot_tls_certificates.sh` | Create TLS certs for mongot pods |
| `09_0316b_create_lb_tls_certificates.sh` | Create TLS certs for Envoy proxy |
| `09_0320_create_mongodb_search_resource.sh` | Create MongoDBSearch CR with `lb.mode: Managed` |
| `09_0325_wait_for_search_resource.sh` | Wait for MongoDBSearch to reach Running phase |

**Verification:**
| Script | Description |
|--------|-------------|
| `09_0326_verify_envoy_deployment.sh` | Verify Envoy proxy is deployed and running |
| `09_0330_show_running_pods.sh` | Show all running pods |
> **Note:** Data import, search index creation, and search query testing are in the shared
> [`08-search-sharded-query-usage`](../08-search-sharded-query-usage/) module, which is reusable
> across all sharded search scenarios.

**Cleanup:**
| Script | Description |
|--------|-------------|
| `09_9010_delete_namespace.sh` | Delete namespace and all resources (manual only) |
