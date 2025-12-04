---
kind: fix
date: 2025-12-02
---

* **MongoDB**, **MongoDBOpsManager**: Improve validation for `featureCompatibilityVersion` field in `MongoDB` and `MongoDBOpsManager` spec.
  The field now enforces proper semantic versioning. Previously, invalid semver values could be accepted,
* potentially resulting in incorrect configurations.
