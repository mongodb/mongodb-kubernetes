---
title: Select the MongoDB container image type for static architecture
kind: feature
date: 2026-09-08
---

* Add support for MongoDB Enterprise Server UBI 9 Slim images. For static architecture deployments, select the image type globally with the `operator.mdbDefaultImageType` Helm value or `MDB_DEFAULT_IMAGE_TYPE` environment variable, or per resource with the `mongodb.com/v1.containerImageType` annotation. Supported values are `ubi8`, `ubi9` (default), and `ubi9-slim`. Unsupported values fall back to `ubi9` with an operator log warning, and an unsupported or ignored (non-static architecture) annotation is reported as a warning in the resource status.
