---
title: Migrate MongoDB deployments from VMs to Kubernetes
kind: feature
date: 2026-08-13
---

* **MongoDB**: deployments running on virtual machines can now be migrated into Kubernetes without downtime, by letting a resource span both VM and Kubernetes members while members are moved across.
  * `spec.externalMembers` (and the per-tier `spec.{mongos,configSrv,shard}.externalMembers` equivalents for sharded clusters) declare the members still running on VMs, so the operator includes them in the Ops Manager automation config, in generated connection strings and in MongoDB Search host seeds.
  * Setting the `mongodb.com/migration-dry-run` annotation runs a connectivity validation Job that checks every external member is reachable from inside the cluster before anything is changed. The resource reports progress through `status.migration` and its conditions.
  * `kubectl mongodb migrate-to-mck` generates the `MongoDB` and `MongoDBUser` resources for an existing Ops Manager deployment (replica set or sharded cluster), including TLS, authentication and Prometheus settings, and validates that the source deployment can be represented before writing anything.
