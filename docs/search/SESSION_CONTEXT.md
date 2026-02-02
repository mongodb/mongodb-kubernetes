# Session Context: Sharded Search with External LB PoC

## Branch: anandsyncs/sharded-poc

## Overview

This branch implements a PoC for sharded MongoDB Search with external L7 load balancer support.
The key feature is per-shard mongot deployments with external LB endpoints for each shard.

---

## Go Code Changes (Committed to Branch)

### 1. API Types (`api/v1/search/mongodbsearch_types.go`)

Added new types for external LB configuration:

```go
// LoadBalancerConfig - new field in MongoDBSearchSpec
type LoadBalancerConfig struct {
Mode     LBMode            `json:"mode"` // "Envoy" or "External"
Envoy    *EnvoyConfig      `json:"envoy,omitempty"`
External *ExternalLBConfig `json:"external,omitempty"`
}

// ExternalLBConfig for user-provided L7 LB
type ExternalLBConfig struct {
Endpoint string                   `json:"endpoint,omitempty"` // For ReplicaSet
Sharded  *ShardedExternalLBConfig `json:"sharded,omitempty"`  // For Sharded
}

// ShardedExternalLBConfig - per-shard endpoints
type ShardedExternalLBConfig struct {
Endpoints []ShardEndpoint `json:"endpoints"`
}

// ShardEndpoint maps shard name to LB endpoint
type ShardEndpoint struct {
ShardName string `json:"shardName"`
Endpoint  string `json:"endpoint"`
}
```

Added helper methods:

- `IsExternalLBMode()`, `IsShardedExternalLB()`, `IsReplicaSetExternalLB()`
- `GetShardEndpointMap()`, `GetReplicas()`, `HasMultipleReplicas()`
- `ShardMongotStatefulSetName(shardName)`, `ShardMongotServiceName(shardName)`
- `ShardMongotConfigMapName(shardName)`, `IsMTLSEnabled()`, `TLSCASecretNamespacedName()`

Added `Replicas` field to `MongoDBSource` for multiple mongot pods per shard.
Added `CA` field to `TLS` struct for mTLS support.

### 2. Validation (`api/v1/search/mongodbsearch_validation.go`) - NEW FILE

Validates:

- LB mode must be "Envoy" or "External"
- External config required when mode is External
- Either endpoint or sharded.endpoints must be specified
- No duplicate shard names in endpoints
- `ValidateShardEndpointsForCluster(shardNames)` - validates all shards have endpoints

### 3. Sharded Enterprise Search Source (`controllers/searchcontroller/sharded_enterprise_search_source.go`) - NEW FILE

Key type: `ShardedEnterpriseSearchSource` implements per-shard mongot deployment.

Key methods:

- `GetMongotStatefulSet(shardName)` - returns StatefulSet for specific shard
- `GetMongotService(shardName)` - returns Service for specific shard
- `GetMongotConfigMap(shardName)` - returns ConfigMap for specific shard
- `buildMongotConfig(shardName)` - builds mongot YAML config with shard-specific settings
- `GetSearchParameters(shardName)` - returns mongod setParameter for specific shard

The mongot config includes:

- `mongod.uri` pointing to shard's mongod
- `mongod.tlsMode` and TLS settings when enabled
- `mongod.auth` with username/password from secret

### 4. Search Construction (`controllers/searchcontroller/search_construction.go`)

Modified `NewSearchSource()` to detect sharded external LB and return `ShardedEnterpriseSearchSource`.

### 5. Reconcile Helper (`controllers/searchcontroller/mongodbsearch_reconcile_helper.go`)

Modified to handle per-shard resources:

- `reconcileShardedExternalLB()` - creates per-shard StatefulSets, Services, ConfigMaps
- `reconcileSearchParameters()` - sets mongod parameters per shard
- `cleanupShardedResources()` - removes orphaned shard resources

### 6. MongoDB Controllers

- `controllers/operator/mongodbshardedcluster_controller.go` - triggers Search reconcile
- `controllers/operator/mongodbreplicaset_controller.go` - triggers Search reconcile
- `controllers/operator/mongodbsearch_controller.go` - handles sharded external LB flow

---

## Shell Script Changes (This Session)

### Scripts Updated (MongoDB operations now go through mongos):

1. `03-search-query-usage/code_snippets/03_0421_import_movies_to_shards.sh`
2. `03-search-query-usage/code_snippets/03_0431_create_search_index_on_shards.sh`
3. `03-search-query-usage/code_snippets/03_0441_wait_for_search_index_ready_on_shards.sh`
4. `03-search-query-usage/code_snippets/03_0446_list_search_indexes_on_shards.sh`
5. `03-search-query-usage/code_snippets/03_0451_execute_search_query_on_shards.sh`
6. `05-search-sharded-enterprise-external-lb/code_snippets/05_0350_import_sample_data.sh`
7. `05-search-sharded-enterprise-external-lb/code_snippets/05_0355_create_search_index_on_shards.sh`
8. `05-search-sharded-enterprise-external-lb/code_snippets/05_0360_wait_for_search_index_ready.sh`
9. `05-search-sharded-enterprise-external-lb/code_snippets/05_0365_execute_search_query_via_mongos.sh`
10. `05-search-sharded-enterprise-external-lb/code_snippets/05_0370_verify_search_results_from_all_shards.sh`

### New Vector Search Scripts Created:

1. `05_0356_create_vector_search_index.sh` - Creates vector index through mongos
2. `05_0361_wait_for_vector_search_index_ready.sh` - Waits for vector index ready

### Scripts NOT Changed (legitimately need per-shard loops for K8s/config operations):

- `05_0320_create_mongodb_search_resource.sh`
- `05_0335_show_running_pods.sh`
- `05_0340_verify_mongod_search_config.sh`

---

## Envoy Integration (Latest Session)

### New Scripts Created for Envoy Load Balancing:

1. `05_0316_create_envoy_certificates.sh` - Creates TLS certs for Envoy (server + client)
2. `05_0317_deploy_envoy_configmap.sh` - Deploys Envoy ConfigMap with SNI-based routing
3. `05_0318_deploy_envoy.sh` - Deploys Envoy Deployment and per-shard proxy Services

### Updated Scripts:

1. `05_0320_create_mongodb_search_resource.sh` - Now uses Envoy proxy endpoints (port 27029)
2. `test.sh` - Added Envoy deployment steps before MongoDBSearch creation
3. `env_variables.sh` - Added ENVOY_IMAGE and ENVOY_PROXY_PORT variables

### Envoy Architecture:

```
mongod -> Envoy proxy (port 27029) -> mongot (port 27028)
          (SNI-based routing)
```

Traffic flow:

1. mongod connects to `<resource>-mongot-<shard>-proxy-svc:27029` (Envoy)
2. Envoy extracts SNI from TLS handshake
3. Envoy routes to appropriate `<resource>-mongot-<shard>-svc:27028` (mongot)
4. Envoy load balances across multiple mongot pods per shard

### Reference Branch:

`lsierant/search-envoy` - Contains the Envoy implementation for MongoDB Search

---

## Remaining Work

### 1. Vector Search Query Script

Create `05_0366_execute_vector_search_query.sh` - reference: `03_0455_execute_vector_search_query.sh`

### 2. Python E2E Test

File: `docker/mongodb-kubernetes-tests/tests/search/search_sharded_enterprise_external_lb.py`

- Currently reverted to original (no TLS changes)
- May need TLS support added back if tests require it

---

## Key Files to Review

```
api/v1/search/mongodbsearch_types.go          # API types with LB config
api/v1/search/mongodbsearch_validation.go     # Validation logic
controllers/searchcontroller/sharded_enterprise_search_source.go  # Per-shard deployment
controllers/searchcontroller/mongodbsearch_reconcile_helper.go    # Reconciliation
controllers/searchcontroller/search_construction.go               # Source factory
```

## Example MongoDBSearch CR for Sharded External LB

```yaml
apiVersion: mongodb.com/v1
kind: MongoDBSearch
metadata:
  name: mdb-sh
spec:
  source:
    mongodb:
      name: mdb-sh
    replicas: 1
  lb:
    mode: External
    external:
      sharded:
        endpoints:
          - shardName: mdb-sh-0
            endpoint: mdb-sh-mongot-mdb-sh-0-svc.NAMESPACE.svc.cluster.local:27028
          - shardName: mdb-sh-1
            endpoint: mdb-sh-mongot-mdb-sh-1-svc.NAMESPACE.svc.cluster.local:27028
```

## Verification Commands

```bash
# Check shell scripts for remaining direct shard connections
grep -rn "for i in.*MDB_SHARD_COUNT" docs/search/*/code_snippets/*.sh

# View all changed files in branch
git diff origin/master...HEAD --name-only

# Run syntax check on shell scripts
bash -n docs/search/05-search-sharded-enterprise-external-lb/code_snippets/*.sh
```
