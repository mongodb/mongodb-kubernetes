---
kind: fix
date: 2026-10-23
---

* **MongoDBSearch**: A `MongoDBSearch` using x509 authentication now fails with a clear message when its source `MongoDB` has no `X509` auth mode, including a source with no authentication modes at all, instead of deploying a `mongot` that cannot connect.
* **MongoDBSearch**: A `MongoDBSearch` that authenticates to its source with SCRAM or x509 now fails with a clear message when the source `MongoDB` has authentication disabled, instead of passing the mode check on a mode list the source would not configure.
* **MongoDBSearch**: Rotating the sync-source password `Secret` or the client-certificate `Secret` now restarts the `mongot` pods instead of leaving them on the old credentials.
* **MongoDBSearch**: After you upgrade the operator, `mongot` pods of a `MongoDBSearch` that uses a sync-source password restart once, because the password now participates in the config hash.
