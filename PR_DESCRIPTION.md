## [WIP] Multi-Cluster Replica Set Support in MongoDB CRD

Part of epic [CLOUDP-235689](https://jira.mongodb.org/browse/CLOUDP-235689) - unifying single-cluster and multi-cluster replica set configuration into the MongoDB CRD.

⚠️ **Status**: Core functionality is **implemented and working**. Multi-cluster replica sets can deploy and reach Running phase. This PR is marked WIP because some robustness features (cross-cluster watches, health monitoring) are not yet implemented.

---

## Demo: Multi-Cluster Replica Set in Action

Here's what this PR enables - deploying a replica set across 3 Kubernetes clusters with a single MongoDB resource:

```bash
# Apply a multi-cluster replica set configuration
kubectl apply -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDB
metadata:
  name: multi-replica-set
  namespace: mongodb-test
spec:
  type: ReplicaSet
  topology: MultiCluster
  version: 7.0.18
  clusterSpecList:
    - clusterName: kind-e2e-cluster-1
      members: 1
    - clusterName: kind-e2e-cluster-2
      members: 1
    - clusterName: kind-e2e-cluster-3
      members: 1
  opsManager:
    configMapRef:
      name: my-project
  credentials: my-credentials
EOF
```

After reconciliation completes, the resource reaches Running phase:

```bash
$ kubectl get mongodb multi-replica-set -n mongodb-test
NAME                 PHASE     VERSION   AGE
multi-replica-set    Running   7.0.18    60m
```

Each member cluster has its own StatefulSet with MongoDB pods running:

```bash
$ kubectl --context kind-e2e-cluster-1 get pods -n mongodb-test -l app=multi-replica-set-svc
NAME                   READY   STATUS    RESTARTS   AGE
multi-replica-set-0-0  2/2     Running   0          58m

$ kubectl --context kind-e2e-cluster-2 get pods -n mongodb-test -l app=multi-replica-set-svc
NAME                   READY   STATUS    RESTARTS   AGE
multi-replica-set-1-0  2/2     Running   0          58m

$ kubectl --context kind-e2e-cluster-3 get pods -n mongodb-test -l app=multi-replica-set-svc
NAME                   READY   STATUS    RESTARTS   AGE
multi-replica-set-2-0  2/2     Running   0          58m
```

Note the stable naming: `multi-replica-set-{clusterIndex}-{podNum}` - powered by the ClusterMapping persisted in annotations:

```bash
$ kubectl get mongodb multi-replica-set -n mongodb-test -o jsonpath='{.metadata.annotations.mongodb\.com/v1\.clusterMapping}'
{"kind-e2e-cluster-1":0,"kind-e2e-cluster-2":1,"kind-e2e-cluster-3":2}
```

---

## Why This Change?

### Current Problem

Today, users must choose between two different CRDs based on their topology:
- **`MongoDB`** with `type: ReplicaSet` - for single Kubernetes cluster
- **`MongoDBMultiCluster`** - for multi Kubernetes cluster deployments

This creates several issues:
- **User confusion**: Which CRD should I use? What if I want to migrate later?
- **Code duplication**: Two separate controllers with overlapping logic
- **Migration barriers**: Changing topology requires recreating the entire resource

Sharded clusters already solved this problem by using a `topology` field in the MongoDB CRD. This PR applies the same pattern to replica sets.

---

## How This Enables Multi-Cluster Replica Sets

### The Core Challenge

In multi-cluster mode, we need to:
1. Deploy one StatefulSet per member cluster (e.g., 3 clusters = 3 StatefulSets)
2. Give each StatefulSet a stable name that doesn't change when clusters are added/removed
3. Track which clusters have which replica counts to handle scale-downs correctly
4. Generate a single Ops Manager automation config that describes the entire topology

### The ClusterMapping Solution

This PR introduces `ClusterMapping` - a persistent map from cluster name → stable index:

```json
{
  "cluster-us-east": 0,
  "cluster-eu-west": 1,
  "cluster-ap-south": 2
}
```

**Why this matters:**
- StatefulSet names use the index: `my-rs-0`, `my-rs-1`, `my-rs-2`
- Indexes are assigned once and never change, even if clusters are removed
- If we remove `cluster-eu-west` and later add `cluster-ca-central`, the new cluster gets index 3 (not 1)
- This prevents StatefulSet name collisions and ensures MongoDB process names remain stable

This follows the **same pattern** that `MongoDBMultiCluster` uses today.

### State Management

The replica set controller historically used annotations to store state directly on the CR. This PR moves to a structured `ReplicaSetDeploymentState` that tracks:

1. **`LastAchievedSpec`**: What the spec looked like when we last reached Running state
   - Used to detect when users make changes that require multi-stage reconciliation
   - Follows the pattern from sharded clusters and AppDB

2. **`ClusterMapping`**: The stable cluster name → index mapping described above
   - Persisted in annotations for now (will move to ConfigMap in future work)
   - Written on EVERY reconciliation to ensure it's never lost

3. **`LastAppliedMemberSpec`**: Per-cluster replica counts from the last reconciliation
   - Example: `{"cluster-us-east": 3, "cluster-eu-west": 5}`
   - Used to detect scale-downs so we can remove members from Ops Manager correctly
   - Without this, we wouldn't know if a cluster went from 5→3 replicas or was just added with 3

> **Important Note on State Storage**
>
> There was debate within the epic team about whether to use annotations vs ConfigMaps for state persistence. Given the uncertainty and desire to move quickly, state is currently serialized to **annotations** for this PR.
>
> However, the **ultimate goal is to migrate to ConfigMap** (like sharded clusters and AppDB do). This will provide:
> - Better scalability for large state
> - Cleaner separation of concerns
> - Consistency across all MongoDB controller types
>
> The structured `ReplicaSetDeploymentState` makes this migration straightforward - we just need to change the serialization target, not the reconciliation logic.

---

## Following Existing Patterns

### Helper Pattern (from Sharded Cluster Controller)

The replica set controller was previously a flat struct with all logic in the main `Reconcile()` method. This PR refactors to the **helper pattern** that the sharded cluster controller uses:

```go
// ReconcileMongoDbReplicaSet - the controller (shared, immutable)
//   ↓
// ReplicaSetReconcilerHelper - per-reconciliation state and methods
//   ↓
// Uses deploymentState for persistence
```

**Why this pattern?**
- Controller runtime shares a single controller instance across all reconciliations
- We can't put mutable state (like `deploymentState`) in the controller struct
- The helper is created fresh for each reconcile, holds the state, and is discarded after
- This matches what sharded clusters do and makes the code easier to reason about

### Member Cluster Iteration

For multi-cluster support, we need to iterate over clusters in a consistent order. This PR introduces `initializeMemberClusters()` which builds an ordered list of `MemberCluster` objects:

```go
type MemberCluster struct {
    Name           string        // e.g., "cluster-us-east"
    Index          int           // from ClusterMapping, e.g., 0
    Members        int           // desired replica count
    Client         client.Client // Kubernetes client for this cluster
    Legacy         bool          // true for single-cluster mode
}
```

**For single-cluster mode:**
- Creates one member cluster with `Name: "__default"` and `Legacy: true`
- Legacy mode preserves existing naming: `my-rs-0`, `my-rs-1` (no cluster index)
- Zero breaking changes to existing single-cluster deployments

**For multi-cluster mode:**
- Creates one member cluster per entry in `ClusterSpecList`
- Names include cluster index: `my-rs-0-0`, `my-rs-0-1` for cluster 0
- Stable ordering based on ClusterMapping ensures consistent reconciliation

This pattern is **inspired by** sharded clusters, but adapted for replica sets (sharded clusters iterate over shards, we iterate over clusters).

---

## What Works in This PR

✅ **API validation**: You can create a MongoDB with `topology: MultiCluster` and `ClusterSpecList`
✅ **State management**: ClusterMapping and LastAppliedMemberSpec are persisted and read correctly
✅ **Member cluster ordering**: Clusters are iterated in stable order with proper client management
✅ **StatefulSet deployment per cluster**: Each member cluster gets its own StatefulSet with correct naming (`my-rs-0`, `my-rs-1`, etc.)
✅ **Automation config generation**: Multi-cluster process lists are built with correct hostnames and registered with Ops Manager
✅ **Agent certificate distribution**: Agent API keys are replicated to all healthy member clusters
✅ **CA ConfigMap synchronization**: TLS CA ConfigMaps are synced from central to member clusters
✅ **Scale operations**: Scale-down detection and preparation works via LastAppliedMemberSpec tracking
✅ **Scaler logic**: Per-cluster replica count calculation and gradual scaling
✅ **Backward compatibility**: Single-cluster replica sets use legacy mode with no breaking changes
✅ **Unit tests**: State management and initialization logic tested
✅ **E2E test**: `e2e_multi_cluster_new_replica_set_scale_up` expects Phase.Running and validates StatefulSets across clusters

---

## What Doesn't Work Yet

❌ **Cross-cluster StatefulSet watches** (TODO at `mongodbreplicaset_controller.go:1185`)
- Drift detection when users manually modify StatefulSets in member clusters
- MongoDBMultiCluster has this - needs porting to unified controller
- Blocker: May need to add `MongoDBMultiResourceAnnotation` to StatefulSets

❌ **Member cluster health monitoring** (TODO at `mongodbreplicaset_controller.go:1194`)
- Automatic reconciliation when member clusters become unavailable
- Current code marks clusters as unhealthy but doesn't watch for health changes
- Need to integrate memberwatch.WatchMemberClusterHealth
- **Note**: Overall management of healthy/unhealthy member clusters needs review (Jira ticket opened)

❌ **Dynamic member list via ConfigMap** (TODO at `mongodbreplicaset_controller.go:1205`)
- Runtime updates to which clusters participate without restarting operator
- Uses util.MemberListConfigMapName pattern from MongoDBMultiCluster

❌ **Multi-cluster validations** (TODO at `mongodbreplicaset_controller.go:387`)
- Some validation rules assume single-cluster topology
- Need to audit ProcessValidationsOnReconcile for multi-cluster cases
- Additional TODO at line 392: implement blockNonEmptyClusterSpecItemRemoval protection

❌ **ConfigMap state migration**
- Currently using annotations for ClusterMapping/LastAppliedMemberSpec
- Should move to ConfigMap like sharded clusters for better scalability
- Requires backwards compatibility for deployments mid-rollout

❌ **Limited test coverage for scaling operations**
- Current E2E test only validates: initial deployment (1,1,1 replicas) → scale up by 2 members
- Not tested: scale down, complex multi-stage scaling, cluster addition/removal
- TODO at line 314: unit test createMemberClusterListFromClusterSpecList

⚠️ **Minor TODOs**
- Line 1347: Verify updateStatus usage is correct in multi-cluster context

---

## How to Pick Up This Work

The core functionality is complete, but there are robustness and operational features to add:

### Option 1: Add Cross-Cluster Watches (Recommended starting point)

**Why**: Enables automatic reconciliation when StatefulSets are manually modified in member clusters.

1. Look at `configureMultiCluster()` at line 1185 - there's a TODO with example code
2. Port the watch setup from MongoDBMultiCluster controller
3. Use `PredicatesForMultiStatefulSet()` to filter events
4. Test by manually deleting a StatefulSet in a member cluster and verify it gets recreated

**Blocker**: Might need to add `MongoDBMultiResourceAnnotation` to StatefulSets created by unified controller.

### Option 2: Member Cluster Health Monitoring

**Why**: Automatically detects when member clusters become unreachable and skips them during reconciliation.

1. Review `configureMultiCluster()` at line 1194 - commented-out implementation exists
2. Adapt `memberwatch.WatchMemberClusterHealth` to handle MongoDB (not just MongoDBMultiCluster)
3. Integrate with existing `mc.Healthy` flag that's already checked in `replicateAgentKeySecret()` and `buildReachableHostnames()`
4. Test by simulating member cluster outage (stop API server or firewall it)

**Current state**: Code already filters unhealthy clusters when building processes and replicating secrets, just needs the watcher.

### Option 3: ConfigMap State Migration

**Why**: Better for large deployments, cleaner separation of concerns, aligns with sharded clusters.

1. Create ConfigMap-based state store (see sharded cluster's pattern)
2. Update `readState()` and `writeClusterMapping()` to use ConfigMap instead of annotations
3. Add migration logic: if ConfigMap doesn't exist but annotations do, copy annotations → ConfigMap
4. Keep LastAchievedSpec in annotations (like sharded clusters do)

**Caution**: Need backwards compatibility - deployments mid-rollout will have old annotations.

### Option 4: Multi-Cluster Validation Audit

**Why**: Ensure all validation rules work correctly for multi-cluster topology.

1. Start at line 387 - there's a TODO: `adapt validations to multi cluster`
2. Grep for all validations in `mongodb_validation.go`
3. Check each one for assumptions about single-cluster (e.g., Spec.Members must be non-zero)
4. Add multi-cluster branches where needed
5. Implement `blockNonEmptyClusterSpecItemRemoval` protection (TODO at line 392)
6. Write unit tests for multi-cluster validation edge cases

### Option 5: Expand Test Coverage for Scaling

**Why**: Validate scale-down, cluster addition/removal, and complex scaling scenarios.

1. Create E2E test for scale-down (e.g., 3,3,3 → 2,2,2)
   - Verify LastAppliedMemberSpec tracking works correctly
   - Confirm members are properly removed from Ops Manager automation config
2. Test cluster addition: start with 2 clusters, add a 3rd later
   - Verify ClusterMapping assigns new stable index
   - Check StatefulSet naming and DNS resolution
3. Test cluster removal: start with 3 clusters, remove one
   - Ensure ClusterMapping retains removed cluster's index (doesn't reassign)
   - Verify automation config removes processes from deleted cluster
4. Test uneven scaling (e.g., 1,3,5 → 2,4,6)

**Current gap**: Only deployment + scale up by 2 is tested

### Option 6: Unit Test Member Cluster List Creation

**Why**: Core function for multi-cluster iteration needs comprehensive unit tests.

1. Review `createMemberClusterListFromClusterSpecList()` - used by all multi-cluster controllers
2. Write unit tests covering:
   - Empty cluster spec list (edge case)
   - Single cluster (should behave like single-cluster mode)
   - Multiple clusters with varying replica counts
   - ClusterMapping consistency (stable indexes)
   - Ordering guarantees
3. Test helper initialization in `initializeMemberClusters()`

**TODO reference**: Line 314 in `mongodbreplicaset_controller.go`

---

## Review Focus

This is a large PR (~1500 lines) with functional multi-cluster support. Please focus on:

### Critical Review Areas

1. **State management correctness**
   - `readState()` / `writeClusterMapping()` / `writeLastAchievedSpec()` separation
   - Is ClusterMapping guaranteed to be persisted on every reconciliation?
   - Migration path from existing single-cluster deployments (Status.Members → LastAppliedMemberSpec)

2. **Backward compatibility**
   - Legacy mode (`mc.Legacy=true`) for single-cluster - does it preserve exact behavior?
   - Can you spot any breaking changes in StatefulSet names, service names, or process names?
   - Validation changes (line 327 in `mongodb_validation.go`) - is allowing `members: 0` safe?

3. **Multi-cluster reconciliation logic**
   - `reconcileStatefulSets()` loop - what happens if one cluster fails? (Hint: current behavior stops immediately)
   - `buildReachableHostnames()` / `filterReachableProcessNames()` - are unhealthy clusters correctly excluded?
   - Scaler logic - is the synthetic ClusterSpecList for single-cluster mode correct?

4. **Ops Manager integration**
   - Process naming in `buildMultiClusterProcesses()` - format is `{name}-{clusterIndex}-{podNum}`
   - Does this match the DNS hostname generation in `GetMultiClusterProcessHostnames()`?
   - Stable process IDs - `getReplicaSetProcessIdsFromDeployment()` preserves IDs across reconciliations?

### Nice-to-Review Areas

- Helper pattern implementation - is the controller/helper split clear?
- `GetReplicaSetStsName()` / `GetReplicaSetServiceName()` - naming conventions match expectations?
- E2E test design - does it adequately test multi-cluster functionality?
- Comments and documentation - are complex sections (like ClusterMapping) explained well?

### Known Issues (Don't Focus On These)

- TODOs for watches and health monitoring (documented in "What Doesn't Work Yet")
- ConfigMap migration (keeping annotations for now is intentional)
- Some code duplication between single/multi-cluster paths (will refactor later)

---

## Testing

```bash
# Unit tests for state management and helper pattern
go test ./controllers/operator -run TestReplicaSet -v

# E2E test for multi-cluster replica set
# Creates 3-cluster deployment, validates StatefulSets, expects Phase.Running
make e2e_multi_cluster_new_replica_set_scale_up
```

**E2E Test Flow** (`multi_cluster_new_replica_set_scale_up.py`):
1. Deploys MongoDB with `topology: MultiCluster` and 3 clusters (1, 1, 1 replicas)
2. Verifies StatefulSet created in each member cluster with correct replica count
3. Asserts deployment reaches `Phase.Running` (full reconciliation success)

**Manual Testing**:
```yaml
apiVersion: mongodb.com/v1
kind: MongoDB
metadata:
  name: my-multi-rs
spec:
  version: 7.0.0-ent
  type: ReplicaSet
  topology: MultiCluster
  clusterSpecList:
    - clusterName: cluster-1
      members: 3
    - clusterName: cluster-2
      members: 2
  opsManager:
    configMapRef:
      name: my-project
  credentials: my-credentials
```

Check ClusterMapping: `kubectl get mongodb my-multi-rs -o jsonpath='{.metadata.annotations.mongodb\.com/cluster-mapping}'`

---

## Related Tickets

- Epic: [CLOUDP-235689](https://jira.mongodb.org/browse/CLOUDP-235689)
- Completed: [CLOUDP-353897](https://jira.mongodb.org/browse/CLOUDP-353897) (StatefulSet deployment) ✅
- Completed: [CLOUDP-353898](https://jira.mongodb.org/browse/CLOUDP-353898) (Automation config) ✅
- Completed: [CLOUDP-353896](https://jira.mongodb.org/browse/CLOUDP-353896) (Agent certs) ✅
- Follow-up: Cross-cluster watches (not ticketed yet)
- Follow-up: Member cluster health monitoring (not ticketed yet)
