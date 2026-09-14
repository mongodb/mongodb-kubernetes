---
title: Forward migration between a multi-cluster AppDB and a MongoDBMultiCluster external AppDB
kind: feature
date: 2026-09-14
---

* **MongoDBOpsManager**: forward migration now supports multi-cluster AppDBs — when `spec.externalApplicationDatabaseRef` points at a `MongoDBMultiCluster`, the operator detaches the internal AppDB StatefulSet in every member cluster and hands ownership to the referenced resource per cluster; topology-incompatible combinations (single-cluster internal AppDB with a multi-cluster external AppDB, and vice versa) are rejected at admission.
* **MongoDBMultiCluster**: `spec.role: AppDB` resources now claim the shared AppDB credentials, adopt the detached per-cluster StatefulSets via the migration handshake, and distribute the shared password and keyfile secrets to their member clusters.
