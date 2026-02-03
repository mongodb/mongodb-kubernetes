---
kind: feature
date: 2026-02-02
---

* **MongoDBOpsManager**: Added field `spec.backup.s3Stores[].objectLock` for specifying whether the S3 bucket has Object Lock enabled. This flag is only allowed for Ops Manager version >= 8.0.19.
