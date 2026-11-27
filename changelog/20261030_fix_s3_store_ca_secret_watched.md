---
kind: fix
date: 2026-10-30
---

* **MongoDBOpsManager**: Fixed a bug where changing a Secret referenced by `spec.backup.s3Stores[].customCertificateSecretRefs` or `spec.backup.s3OpLogStores[].customCertificateSecretRefs` did not update the CA certificate in Ops Manager until the next reconcile.
