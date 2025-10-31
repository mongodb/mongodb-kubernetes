# MongoDB Controllers for Kubernetes (MCK)

A Kubernetes operator that manages MongoDB deployments (Community and Enterprise editions) across one or multiple Kubernetes clusters. Supports replica sets, sharded clusters, and single-node instances with Ops Manager integration for Enterprise.

## Operator Design Principles

### Single Source of Truth
The CRD spec declares intent; status reflects reality. Never encode intent in annotations or separate state. The operator's job is to converge reality (Kubernetes resources + external systems) to match the spec, safely and idempotently.

### Reconciliation Safety
Every reconciliation step must be idempotent and safe on retries. Failures should be observable and recoverable without manual resource mutation. Reconciliation must be deterministic and resumable at any point.

### Backwards Compatibility
Old custom resources and controllers must continue working during rollouts. API changes require explicit conversion logic and round-trip fidelity tests. Deprecation follows documented windows.

### Testing Requirements
- Idempotency: Reconcile same resource multiple times, verify no unexpected changes
- Golden-resource diffs: Validate no spec drift for core resources during migrations
- Fault injection: Crash mid-reconcile, delete state mid-operation - must self-heal
- Conversion fidelity: Round-trip tests for all API version conversions

## Repository Structure

### Custom Resource Definitions (CRDs)
- **MongoDB** (`api/v1/mdb/mongodb_types.go`) - Main CRD supporting replica sets, sharded clusters, and single-node deployments across one or multiple Kubernetes clusters
- **MongoDBOpsManager** (`api/v1/om/opsmanager_types.go`) - Deploys and manages Ops Manager instances
- **MongoDBMultiCluster** (`api/v1/mdbmulti/`) - Legacy multi-cluster replica set CRD (being deprecated in favor of MongoDB with topology field)
- **MongoDBUser** (`api/v1/user/mongodbuser_types.go`) - Database user management
- **MongoDBSearch** (`api/v1/search/mongodbsearch_types.go`) - MongoDB Atlas Search integration

### Controllers
Located in `controllers/operator/`:
- **mongodbreplicaset_controller.go** - Reconciles MongoDB CRD when type=ReplicaSet
- **mongodbshardedcluster_controller.go** - Reconciles MongoDB CRD when type=ShardedCluster
- **mongodbstandalone_controller.go** - Reconciles MongoDB CRD when type=Standalone (legacy, rarely used)
- **mongodbopsmanager_controller.go** - Reconciles MongoDBOpsManager CRD
- **mongodbuser_controller.go** - Reconciles MongoDBUser CRD
- **mongodbsearch_controller.go** - Reconciles MongoDBSearch CRD

### E2E Testing
- **Location**: `docker/mongodb-kubernetes-tests/tests/`
- **Framework**: pytest
- **Structure**: Test suites organized by feature (multicluster, replicaset, shardedcluster, opsmanager, etc.)

### Evergreen CI
- **Files**: `.evergreen.yml`, `.evergreen-tasks.yml`, `.evergreen-functions.yml`, etc.
- **What it is**: MongoDB's internal CI/CD system (similar to GitHub Actions)
- **MCP Integration**: Evergreen MCP server available for querying test results, patches, and build status

### Context Files
- **Location**: `scripts/dev/contexts/`
- **Purpose**: Environment configuration for local development and CI testing
- **What they control**:
  - Single vs multi-cluster mode
  - Target Kubernetes cluster(s)
  - Ops Manager version
  - Static vs non-static architecture
  - Image versions and registry settings
