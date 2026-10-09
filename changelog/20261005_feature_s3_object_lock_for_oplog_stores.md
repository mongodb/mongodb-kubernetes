---
kind: feature
date: 2026-10-05
---

* **MongoDBOpsManager**: Added support for `objectLockEnabled`, `objectRetentionDays` and `objectRetentionMode` on S3 OpLog stores (`spec.backup.s3OpLogStores`), which were previously rejected by validation. S3 Object Lock on OpLog stores requires Ops Manager >= 8.0.28 (unlike snapshot stores, which support it since 8.0.19); `objectRetentionDays` and `objectRetentionMode` must be specified together and require `objectLockEnabled`.
