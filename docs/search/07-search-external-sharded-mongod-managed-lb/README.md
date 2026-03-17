# MongoDB Search with External Sharded MongoDB + Managed Envoy LB

This guide walks you through deploying **MongoDB Search** against your **existing external MongoDB sharded cluster** (running on VMs, bare metal, or another Kubernetes cluster) using the operator's **managed Envoy load balancer**.

For testing purposes, we include scripts that simulate an external cluster inside Kubernetes. In production, you would skip the "internal" scripts and point the configuration at your real MongoDB hosts.

## For Documentation Team

The following scripts are marked `# AUDIENCE: internal` and should be **excluded** from published docs (test scaffolding only):

| Script | Description |
|--------|-------------|
| `07_0046_create_image_pull_secrets.sh` | Create image pull secrets (private registry, CI only) |
| `07_0101_helm_install_staging_operator.sh` | Install staging/dev operator |
| `07_0300_create_ops_manager_resources.sh` | Create Ops Manager resources (simulated cluster) |
| `07_0304_generate_tls_certificates.sh` | Generate certs for simulated cluster |
| `07_0310_create_external_mongodb_sharded_cluster.sh` | Create simulated external sharded cluster |
| `07_0315_wait_for_external_cluster.sh` | Wait for simulated cluster to reach Running phase |
| `07_0316_create_external_mongodb_users.sh` | Create MongoDB users on simulated cluster |
| `07_9010_delete_namespace.sh` | Delete namespace and all resources (cleanup/teardown) |

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
```

**Note:** There is NO `spec.lb.endpoint` - the operator creates and exposes the endpoints automatically.

### Operator-Created Resources

When you apply the MongoDBSearch CR with `lb.mode: Managed`, the operator creates:

| Resource | Name Pattern | Purpose |
|----------|--------------|---------|
| ConfigMap | `{name}-search-lb-config` | Envoy bootstrap configuration |
| Deployment | `{name}-search-lb` | Envoy proxy pods |
| Service (per shard) | `{name}-search-0-{shardName}-proxy-svc` | SNI routing endpoints |

### Configuring External mongod

Your external mongod instances must be configured to connect to the operator-created proxy Services. The endpoint format is:

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
# Verify proxy Services exist
kubectl get svc -n ${MDB_NS} | grep proxy-svc

# Test connectivity from mongod pod
kubectl exec -it ${MDB_EXTERNAL_SHARD_0_POD} -n ${MDB_NS} -- \
  curl -v ${MDB_PROXY_HOST_SHARD_0}
```

**Common causes:**
- Proxy Services not created - MongoDBSearch may not be in Running phase
- Network policies blocking traffic
- DNS resolution issues

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
| `07_0040_validate_env.sh` | Validate required environment variables |
| `07_0045_create_namespaces.sh` | Create Kubernetes namespace |
| `07_0046_create_image_pull_secrets.sh` | Create image pull secrets (if needed) |
| `07_0090_helm_add_mongodb_repo.sh` | Add MongoDB Helm repository |
| `07_0100_install_operator.sh` | Install MongoDB Kubernetes Operator |
| `07_0300_create_ops_manager_resources.sh` | Create Ops Manager / Cloud Manager resources |

**TLS Configuration:**
| Script | Description |
|--------|-------------|
| `07_0301_install_cert_manager.sh` | Install cert-manager |
| `07_0302_configure_tls_prerequisites.sh` | Create self-signed CA, ClusterIssuer, distribute CA |
| `07_0304_generate_tls_certificates.sh` | Generate certs for shards, config servers, mongos |

**Simulated External Cluster:**
| Script | Description |
|--------|-------------|
| `07_0310_create_external_mongodb_sharded_cluster.sh` | Create simulated external sharded cluster |
| `07_0315_wait_for_external_cluster.sh` | Wait for cluster to reach Running phase |
| `07_0316_create_external_mongodb_users.sh` | Create MongoDB users |

**MongoDB Search with Managed Envoy LB:**
| Script | Description |
|--------|-------------|
| `07_0316a_create_mongot_tls_certificates.sh` | Create TLS certs for mongot pods |
| `07_0316b_create_lb_tls_certificates.sh` | Create TLS certs for Envoy proxy |
| `07_0320_create_mongodb_search_resource.sh` | Create MongoDBSearch CR with `lb.mode: Managed` |
| `07_0325_wait_for_search_resource.sh` | Wait for MongoDBSearch to reach Running phase |

**Verification:**
| Script | Description |
|--------|-------------|
| `07_0326_verify_envoy_deployment.sh` | Verify Envoy proxy is deployed and running |
| `07_0330_show_running_pods.sh` | Show all running pods |
| `07_0335_run_mongodb_tools_pod.sh` | Deploy mongodb-tools pod for DB commands |
| `07_0336_verify_mongod_search_config.sh` | Verify mongod search parameters *(disabled in test.sh)* |
| `07_0337_verify_mongos_search_config.sh` | Verify mongos search parameters *(disabled in test.sh)* |

**Data & Search Testing:**
| Script | Description |
|--------|-------------|
| `07_0340_import_sample_data.sh` | Import sample_mflix dataset and shard collections |
| `07_0345_create_search_index.sh` | Create text search index on movies |
| `07_0346_create_vector_search_index.sh` | Create vector search index on embedded_movies |
| `07_0350_wait_for_search_indexes.sh` | Wait for search indexes to be ready |
| `07_0355_execute_search_query.sh` | Execute text search query |
| `07_0356_execute_vector_search_query.sh` | Execute vector search query |

**Cleanup:**
| Script | Description |
|--------|-------------|
| `07_9010_delete_namespace.sh` | Delete namespace and all resources (manual only) |

> **Note:** Scripts `07_0336`/`07_0337` are currently disabled in `test.sh` pending a fix to read from config files instead of `getParameter`.
