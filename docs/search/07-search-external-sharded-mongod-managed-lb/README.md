# MongoDB Search with External Sharded MongoDB + Managed Envoy LB

This guide walks you through deploying **MongoDB Search** against an **external sharded MongoDB cluster** using the operator's **managed Envoy load balancer**.

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
  name: ext-search
spec:
  replicas: 2  # Multiple mongot replicas per shard for HA
  source:
    username: search-sync-source
    passwordSecretRef:
      name: ext-search-search-sync-source-password
      key: password
    external:
      shardedCluster:
        router:
          hosts:
            - "ext-mdb-sh-mongos-0.ext-mdb-sh-svc.mongodb.svc.cluster.local:27017"
        shards:
          - shardName: ext-mdb-sh-0
            hosts:
              - "ext-mdb-sh-0-0.ext-mdb-sh-sh.mongodb.svc.cluster.local:27017"
          - shardName: ext-mdb-sh-1
            hosts:
              - "ext-mdb-sh-1-0.ext-mdb-sh-sh.mongodb.svc.cluster.local:27017"
      tls:
        ca:
          name: root-secret
  security:
    tls:
      certsSecretPrefix: certs
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
ext-search-search-0-ext-mdb-sh-0-proxy-svc.mongodb.svc.cluster.local:27029
```

Set these mongod parameters on each shard:
```javascript
{
  setParameter: {
    mongotHost: "ext-search-search-0-ext-mdb-sh-0-proxy-svc.mongodb.svc.cluster.local:27029",
    searchIndexManagementHostAndPort: "ext-search-search-0-ext-mdb-sh-0-proxy-svc.mongodb.svc.cluster.local:27029",
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
kubectl describe deployment ext-search-search-lb -n mongodb
kubectl logs -l app=ext-search-search-lb -n mongodb
```

**Common causes:**
- TLS certificate secrets not found - ensure certificates are created first
- ConfigMap not ready - check if `ext-search-search-lb-config` exists
- Image pull issues - check image pull secrets

### mongod Cannot Reach Envoy

**Symptoms:** Search queries fail with connection errors.

**Check:**
```bash
# Verify proxy Services exist
kubectl get svc -n mongodb | grep proxy-svc

# Test connectivity from mongod pod
kubectl exec -it ext-mdb-sh-0-0 -n mongodb -- \
  curl -v ext-search-search-0-ext-mdb-sh-0-proxy-svc.mongodb.svc.cluster.local:27029
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
kubectl get pods -n mongodb | grep search

# Check mongot logs
kubectl logs ext-search-search-0-ext-mdb-sh-0-0 -n mongodb
```

**Common causes:**
- mongot cannot connect to MongoDB (check source credentials)
- TLS CA mismatch between mongod and mongot
- mongot pods not ready yet

### MongoDBSearch Stuck in Pending

**Symptoms:** MongoDBSearch resource doesn't reach Running phase.

**Check:**
```bash
kubectl describe mongodbsearch ext-search -n mongodb
kubectl get events -n mongodb --field-selector involvedObject.name=ext-search
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
| `env_variables_e2e_private.sh` | Automated test overrides (ignore for manual use) |
| `test.sh` | Runner script that executes all snippets in order |
| `code_snippets/` | Individual shell scripts for each step |
