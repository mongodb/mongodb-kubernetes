---
kind: fix
date: 2026-11-18
---

* Added validation to explicitly reject `ubi8` as a container image type for `MongoDB` and `MongoDBMultiCluster` resources using static container architecture when configured using the `mongodb.com/v1.containerImageType` resource annotation or `MDB_DEFAULT_IMAGE_TYPE` operator environment variable. `ubi8` is not supported in static containers and can cause MongoDB to fail to start.
