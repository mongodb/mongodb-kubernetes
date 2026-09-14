---
title: External Application Database references to MongoDBMultiCluster are resolved and watched
kind: feature
date: 2026-09-14
---

* **MongoDBOpsManager**: external Application Database references with `kind: MongoDBMultiCluster` are now resolved, validated (`spec.role: AppDB` on the referenced resource), and watched for changes; the AppDB connection string and TLS/CA settings are computed from the referenced resource.
