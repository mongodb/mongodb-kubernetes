---
kind: feature
date: 2026-11-18
---

* **MongoDBOpsManager**: Added field `portableBackupEnabled` to `spec.backup.s3Stores[]` and `spec.backup.s3OpLogStores[]` for opting into portable backups on Ops Manager >= 8.0.28 (8.0.x) or >= 9.0.2. These versions read the S3 Object Lock settings from the bucket, so the operator no longer sends `objectLockEnabled`, `objectRetentionDays` and `objectRetentionMode` to them. These fields are deprecated, and the `MongoDBOpsManager` resource reports a backup status warning while they are set. Portable backups require S3 Object Lock on the bucket and the `brs.immutableBackupEnabled` Ops Manager setting, and cannot be disabled once enabled.
