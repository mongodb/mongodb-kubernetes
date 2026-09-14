---
title: Reverse migration from a MongoDBMultiCluster external AppDB back to an internal AppDB
kind: feature
date: 2026-09-14
---

* **MongoDBOpsManager**: reverse migration now supports multi-cluster external AppDBs — removing `spec.externalApplicationDatabaseRef` hands the per-cluster StatefulSets back from the `MongoDBMultiCluster` resource cluster by cluster (annotation handshake, no finalizers), reclaims the shared credentials without rotation, and resumes internal management; deleting the external resource first falls back to per-cluster cleanup with the AppDB recreated from retained PVCs.
* **MongoDBMultiCluster**: `spec.role: AppDB` resources release their per-cluster StatefulSets on reverse migration and skip Ops Manager project cleanup on deletion — the project is deliberately left for the user to remove.
