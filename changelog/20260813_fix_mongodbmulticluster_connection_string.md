---
kind: fix
date: 2026-08-13
---

* **MongoDBMultiCluster**: Fixed a bug where the connection string Secret generated for `MongoDBUser` resources referencing a `MongoDBMultiCluster` always used internal `svc.cluster.local` hostnames, ignoring the `externalAccess.externalDomain` configured per cluster or at the top level. The connection string now uses the configured external domains, matching the hostnames registered in Ops Manager.
