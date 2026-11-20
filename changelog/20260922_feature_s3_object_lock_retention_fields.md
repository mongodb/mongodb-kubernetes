---
kind: feature
date: 2026-09-22
---

* **MongoDBOpsManager**: Added fields `spec.backup.s3Stores[].objectRetentionDays` and `spec.backup.s3Stores[].objectRetentionMode` for configuring the S3 Object Lock retention period and mode (`GOVERNANCE` or `COMPLIANCE`) on backup snapshot stores. Both fields require `objectLockEnabled` and Ops Manager >= 8.0.27 (the operator omits them from the Ops Manager API on older versions, where they are rejected); `objectLockEnabled` itself requires Ops Manager >= 8.0.19.
