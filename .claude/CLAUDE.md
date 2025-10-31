# Epic: Multi-Cluster Replica Set Unification

> **Scope**: Only relevant when working on multi-cluster replica set unification. Ignore for all other work.

## Why This Epic Exists

Currently, users must choose between two different CRDs depending on desired topology:
- **`MongoDB`** - for single Kubernetes cluster deployments
- **`MongoDBMultiCluster`** - for multi Kubernetes cluster deployments

This creates:
- **Confusing UX**: Users must decide CRD upfront, can't easily change topology later
- **Maintenance burden**: Separate controllers and reconciliation paths
- **Migration barriers**: Changing topology requires recreating resources

**Sharded clusters already solved this** via a `topology` field in the MongoDB CRD. This epic applies the same pattern to replica sets.

## Epic Goals

**Unify the interface**: Single MongoDB CRD supports both single-cluster and multi-cluster replica sets
**Consolidate logic**: Single reconciliation path handles both topologies consistently
**Enable future migrations**: Lay groundwork for topology changes without resource recreation
**Deprecate MongoDBMultiCluster**: Provide zero-downtime migration path from old CRD to unified MongoDB CRD

## What's Changing

### CRD Level
- MongoDB CRD gains `topology: MultiCluster` support for replica sets
- Multi-cluster configuration fields (cluster spec list) added to MongoDB
- Backwards compatible: existing single-cluster replica sets require no changes (implicit single-cluster)

### Controller Level
- Replica set controller handles both topologies through unified reconciliation
- State management moves from annotations to ConfigMap (aligning with sharded clusters and AppDB)

### Migration Path
Script-based migration enables users to move from MongoDBMultiCluster → MongoDB:
1. Operator stops reconciling old CRD
2. Starts reconciling new MongoDB CRD (resource name compatibility maintained)
3. Old CR deleted without cascading to actual MongoDB infrastructure

## Key Files

**CRDs**:
- `api/v1/mdb/mongodb_types.go` - MongoDB CRD (being extended for multi-cluster)
- `api/v1/mdbmulti/mongodb_multi_types.go` - MongoDBMultiCluster CRD (being deprecated)

**Controllers**:
- `controllers/operator/mongodbreplicaset_controller.go` - Replica set controller (being unified)
- `controllers/operator/mongodbshardedcluster_controller.go` - Reference: already supports topology field

**State Management References**:
- AppDB controller - ConfigMap-based state management
- Sharded cluster controller - ConfigMap-based state management

### Topology Constants
```go
const (
    ClusterTopologySingleCluster = "SingleCluster"
    ClusterTopologyMultiCluster  = "MultiCluster"
)
```

## Testing Strategy
- Reuse existing MongoDBMultiCluster test coverage
- Validate migration scenarios and ConfigMap state correctness
- Create automated snippets for user documentation

---

## Operator Context (for reference)

**Operator**: MongoDB Controllers for Kubernetes (MCK)
**Deploys**: MongoDB replica sets, sharded clusters, and single-node instances across Kubernetes clusters
**Editions**: Community (self-managed) and Enterprise (Ops Manager integration)
