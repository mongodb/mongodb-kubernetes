---
title: Reject externalDomain changes on existing MongoDB resources
kind: fix
date: 2026-08-07
---

* **MongoDB**: the validating webhook now rejects adding, changing or removing an external domain on an already deployed resource. This covers `spec.externalAccess.externalDomain`, the per-tier `spec.{mongos,configSrv,shard}.externalAccess.externalDomain` fields and their per-member-cluster equivalents in `clusterSpecList`. Any of these transitions repoints existing members at new hostnames, which is unsupported and will lead to a Failed resource.
