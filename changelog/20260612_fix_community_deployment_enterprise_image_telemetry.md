---
title: Fix Community deployment enterprise image telemetry
kind: fix
date: 2026-06-12
---

* Fixed a bug where all `MongoDBCommunity` deployment telemetry rows incorrectly reported `IsRunningEnterpriseImage = true`. The field was being evaluated against the operator-level enterprise image rather than the image configured in each CR's spec, causing a misleading ~100% enterprise rate for the Community deployment type.
* `IsRunningEnterpriseImage` for Community deployments is now derived from the `mongod` container image override in `spec.statefulSet.spec.template.spec.containers`, if present. When no override is set, the field correctly defaults to `false`, reflecting that Community CRs use the community MongoDB server image by default.
