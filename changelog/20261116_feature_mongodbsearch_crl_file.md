---
title: Certificate revocation list support for MongoDB Search
kind: feature
date: 2026-11-16
---

* `MongoDBSearch` gains `spec.source.crlFile`, naming a certificate revocation list file inside the source MongoDB's TLS CA ConfigMap. mongot enforces the list for its sync source and gRPC connections, and the source MongoDB's mongod and mongos enforce it through `net.tls.CRLFile`.
* mongot reads the CRL only at startup and fails closed, so rotating the CRL in the ConfigMap rolls the mongot pods to re-read it. A malformed CRL leaves mongot unready until the file is fixed.
* A rotated CRL does not reach mongod and mongos yet; they need a certificate rotation to pick it up.
