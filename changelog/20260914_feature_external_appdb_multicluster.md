---
title: External AppDB multi-cluster
kind: feature
date: 2026-09-14
---

* **MongoDBOpsManager**: Added multi-cluster support to the external Application Database feature — `spec.externalApplicationDatabaseRef` now accepts `kind: MongoDBMultiCluster`, where the referenced `MongoDBMultiCluster` must set `spec.role: AppDB`. Forward and reverse migration between an internal and an external AppDB are supported per member cluster.
