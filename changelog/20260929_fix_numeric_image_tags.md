---
kind: fix
date: 2026-09-29
---

* **Numeric image tags**: Fixed an issue where the Helm Chart rendered an invalid image reference for the `mongodb-kubernetes-operator` Deployment when the configured image version was an all-digit value, such as a commit SHA. The version is now always rendered as a string, so the Deployment `image` and the `MDB_OPERATOR_IMAGE` environment variable remain valid.
