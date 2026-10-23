---
kind: fix
date: 2026-10-23
---

* **MongoDBSearch**: An x509-auth `MongoDBSearch` referencing a source `MongoDB` without an `X509` auth mode now fails validation instead of deploying and failing at runtime (`mongod` disables the `MONGODB-X509` mechanism). Rotating the sync-source password `Secret` (`spec.source.passwordSecretRef`) or the x509/SCRAM client-certificate `Secret` (`spec.source.x509.clientCertificateSecretRef`, `spec.source.tls.clientCertificateSecretRef`) now triggers a reconcile that restarts the `mongot` StatefulSet.
