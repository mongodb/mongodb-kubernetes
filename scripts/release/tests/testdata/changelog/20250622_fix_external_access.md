---
title: External Access Fix
kind: fix
date: 2025-06-22
---

* **MongoDB**: Operator now correctly applies the external service customization based on `spec.externalAccess` and `spec.mongos.clusterSpecList.externalAccess` configuration. Previously it was ignored, but only for Multi Cluster Sharded Clusters.
