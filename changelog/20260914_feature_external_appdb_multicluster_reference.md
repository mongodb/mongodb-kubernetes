---
title: External Application Database references now accept MongoDBMultiCluster
kind: feature
date: 2026-09-14
---

* **MongoDBOpsManager**: `spec.externalApplicationDatabaseRef.kind` now accepts `MongoDBMultiCluster` for external Application Database references.
* **MongoDBMultiCluster**: `spec.role: AppDB` is now admitted with CEL guards enforcing ReplicaSet-only, SCRAM-only with `ignoreUnknownUsers`, at least three members across `clusterSpecList`, and immutable `spec.role`.
