---
kind: feature
date: 2026-03-24
---

* **MongoDBSearch:** Added x509 authentication support from `mongot` to `mongod`. The field `spec.source.x509`
    of `MongoDBSearch` resource can be used to configure the `mongot` client certificate.
