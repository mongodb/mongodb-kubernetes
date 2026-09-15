---
title: Multi-cluster external AppDB e2e coverage and Evergreen variant
kind: other
date: 2026-09-15
---

* **MongoDBOpsManager**: the external-AppDB e2e suites (fresh start, forward migration, backup and restore) now run in both single-cluster and multi-cluster modes from the same tests, and a new `e2e_multi_cluster_om80_kind_ubi` Evergreen variant runs the multi-cluster scenarios against Ops Manager 8.0.
