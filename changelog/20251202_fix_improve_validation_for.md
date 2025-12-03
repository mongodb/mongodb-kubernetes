---
kind: fix
date: 2025-12-02
---

* **MongoDB**, **MongoDBOpsManager**: Improve validation for `featureCompatibilityVersion` field in `MongoDB` and `MongoDBOpsManager` spec.
  Previously we didn't validate the semver version format, which could lead to applying incorrect configuration.
