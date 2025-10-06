---
kind: feature
date: 2025-09-25
---

* Add support for custom cluster domains via `CLUSTER_DOMAIN` environment variable and `ClusterDomain` spec field
  * Replace hardcoded `cluster.local` value with configurable cluster domain
  * Add optional `ClusterDomain` field to `MongoDBCommunitySpec` with hostname format validation
  * Implement `GetClusterDomain()` method with fallback hierarchy: spec field → environment variable → default `cluster.local`
  * Update MongoDB connection URI generation (standard and SRV) to use configurable domain
  * Fix host seed generation in `CommunitySearchSource` and `EnterpriseResourceSearchSource` to use configurable domain
  * Update mongod configuration parameters for MongoDB Search with correct cluster domain
  * Remove `clusterDomain` parameter from public methods (`MongoURI()`, `MongoSRVURI()`, `MongoAuthUserURI()`, `MongoAuthUserSRVURI()`) - now retrieve domain directly from spec
  * Update all unit tests to reflect new structure
