---
kind: feature
date: 2025-09-25
---

* **MongoDBCommunity**: Add support for custom cluster domains via `CLUSTER_DOMAIN` environment variable and `ClusterDomain` spec field
  * Replace hardcoded `cluster.local` value with configurable cluster domain throughout the codebase
  * Add optional `ClusterDomain` field to `MongoDBCommunitySpec` with hostname format validation in CRD
  * Implement `GetClusterDomain()` method in `mongodbcommunity_types.go:1149` with fallback hierarchy: spec field → `CLUSTER_DOMAIN` environment variable → default `cluster.local`
  * Update MongoDB connection URI generation to use configurable domain:
    - `MongoURI()` in `mongodbcommunity_types.go:927`
    - `MongoSRVURI()` in `mongodbcommunity_types.go:934`
    - `MongoAuthUserURI()` in `mongodbcommunity_types.go:941`
    - `MongoAuthUserSRVURI()` in `mongodbcommunity_types.go:955`
  * Update `Hosts()` method in `mongodbcommunity_types.go:967` to use configurable cluster domain
  * Fix host seed generation to use configurable domain:
    - `CommunitySearchSource.HostSeeds()` in `community_search_source.go:27`
    - `EnterpriseResourceSearchSource.HostSeeds()` in `enterprise_search_source.go:25`
  * Update MongoDB Search mongod configuration:
    - `GetMongodConfigParameters()` in `mongodbsearch_reconcile_helper.go:391` now accepts `clusterDomain` parameter
    - `mongotHostAndPort()` in `mongodbsearch_reconcile_helper.go:406` now accepts `clusterDomain` parameter
  * Update replica set controller:
    - `buildAutomationConfig()` in `replica_set_controller.go:533` uses `mdb.Spec.GetClusterDomain()`
    - `getMongodConfigSearchModification()` in `replica_set_controller.go:837` accepts and passes `clusterDomain` parameter
    - Remove environment variable reads from `Reconcile()` method in `replica_set_controller.go:237,250`
  * Update `applySearchOverrides()` in `mongodbreplicaset_controller.go:659` to pass cluster domain to `GetMongodConfigParameters()`
  * Remove `clusterDomain` parameter from public URI methods - domain now retrieved directly from spec
  * Update `updateConnectionStringSecrets()` in `mongodb_users.go:44` to remove `clusterDomain` parameter
  * Add `ClusterDomainEnv` constant in `constants.go:14` with value `"CLUSTER_DOMAIN"`
  * Update CRD definitions in both `config/crd/bases/` and `helm_chart/crds/` to include new `clusterDomain` field
  * Update all unit tests in `mongodbcommunity_types_test.go` to use spec field instead of parameter
  * Update E2E tests to remove cluster domain parameter from URI method calls
