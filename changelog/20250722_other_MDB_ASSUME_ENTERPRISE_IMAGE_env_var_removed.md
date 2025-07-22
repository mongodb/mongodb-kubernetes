---
title: MDB_ASSUME_ENTERPRISE_IMAGE environment variable has been removed
kind: other
date: 2025-07-22
---

* The `MDB_ASSUME_ENTERPRISE_IMAGE` environment variable has been removed. This undocumented environment variable, when set to `true`, forced the `-ent` suffix for the database image version in static architecture. If you are mirroring images and were using this variable, ensure that you do not rename the server image. The name must contain `mongodb-enterprise-server`; otherwise, the operator will not function correctly.
