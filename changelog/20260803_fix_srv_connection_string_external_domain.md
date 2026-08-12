---
title: SRV connection strings use the configured external domain
kind: fix
date: 2026-08-03
---

* **MongoDB** (replica sets and sharded clusters): the `connectionString.standardSrv` value now uses `spec.externalAccess.externalDomain` when one is configured, instead of the internal `*.svc.cluster.local` service FQDN which is not reachable from outside the cluster. Provisioning the `_mongodb._tcp` SRV records in that zone is the customer's responsibility, as it already is for the A records `externalAccess` depends on. This completes the change started in CLOUDP-215627, which fixed only the non-SRV connection string. For MongoDBMultiCluster resources, only a top-level `spec.externalAccess.externalDomain` is honoured by this fix; per-cluster `externalDomain` values set in `clusterSpecList` are not yet used when building the SRV connection string.
