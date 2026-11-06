---
kind: feature
date: 2026-10-30
---

* **MongoDBOpsManager**: Added `spec.backup.s3Stores[].customCertificateConfigMapRefs` and `spec.backup.s3OpLogStores[].customCertificateConfigMapRefs` to provide S3 store CA certificates through ConfigMaps, like every other CA in MCK. `customCertificateSecretRefs` keeps working, and both fields can be used together.
