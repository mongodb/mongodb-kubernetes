---
kind: fix
date: 2026-10-23
---

* **MongoDBSearch**: An x509-auth `MongoDBSearch` referencing a source `MongoDB` without an `X509` auth mode (including a source with no authentication modes at all) now fails validation instead of deploying and failing at runtime (`mongod` disables the `MONGODB-X509` mechanism). Rotating the sync-source password `Secret` (an explicit `spec.source.passwordSecretRef` or the default `<name>-<user>-password` secret) or the x509/SCRAM client-certificate `Secret` (`spec.source.x509.clientCertificateSecretRef`, `spec.source.tls.clientCertificateSecretRef`) now triggers a reconcile that restarts the `mongot` StatefulSet. Upgrading the operator restarts the `mongot` pods of an existing `MongoDBSearch` with a sync-source password once, even without a rotation, because the password content now participates in the config hash.
