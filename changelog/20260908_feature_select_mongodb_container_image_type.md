---
title: Select the MongoDB container image type for static architecture and AppDB
kind: feature
date: 2026-09-08
---

* Add support for MongoDB Enterprise Server UBI 9 Slim images for static architecture deployments, and for the Ops Manager Application Database in both architectures. Select the image type operator-wide with the `operator.mdbDefaultImageType` Helm value (`MDB_DEFAULT_IMAGE_TYPE` environment variable), or per resource with the `mongodb.com/v1.containerImageType` annotation on `MongoDB`, `MongoDBMultiCluster`, or `MongoDBOpsManager` (for its Application Database). Supported values are `ubi8`, `ubi9`, and `ubi9-slim`. When no image type is set, existing deployments are unchanged: static architecture deployments use `ubi9` and non-static Application Databases use `ubi8`. An unsupported value falls back to that default, with a warning in the operator log for the Helm value or in the resource status for the annotation. The annotation is ignored, with a status warning, on non-static `MongoDB` and `MongoDBMultiCluster` resources.
