---
kind: feature
date: 2026-11-20
---

* **MongoDBCommunity**: `spec.members` can be set to `0` when `spec.arbiters` is `0`, to stop every Pod of the replica set while keeping the resource, its volumes and its secrets, for example outside working hours. The connection string secrets and `status.mongoUri` keep the last hosts while there is no member, so clients get a working URI again once `spec.members` is set back.
