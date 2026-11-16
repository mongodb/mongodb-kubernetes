---
title: Certificate revocation list support for MongoDB Search
kind: feature
date: 2026-11-16
---

* `MongoDBSearch` gains `spec.source.crlFile`, naming a certificate revocation list file inside the source MongoDB's TLS CA ConfigMap. mongot enforces the list for its sync source and gRPC connections, and the source MongoDB's mongod and mongos enforce it through `net.tls.CRLFile`.
* mongot picks up a rotated CRL without a restart. A rotated CRL on mongod and mongos needs a certificate rotation to take effect.
