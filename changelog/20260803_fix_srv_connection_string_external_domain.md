---
title: SRV connection strings use the configured external domain
kind: fix
date: 2026-08-03
---

* **MongoDB** (replica sets and sharded clusters): The `connectionString.standardSrv` value now uses `spec.externalAccess.externalDomain` when one is configured, so clients outside the cluster can use it. Previously it always returned the internal `*.svc.cluster.local` service FQDN, which is not reachable from outside. Provisioning the `_mongodb._tcp` SRV records in that zone is your responsibility, as it already is for the A records `externalAccess` depends on. For `MongoDBMultiCluster` resources, only a top-level `spec.externalAccess.externalDomain` is used; per-cluster `externalDomain` values in `clusterSpecList` are not yet honoured when building the SRV connection string.
