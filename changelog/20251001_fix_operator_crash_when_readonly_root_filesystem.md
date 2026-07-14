---
kind: fix
date: 2025-10-01
---

* Fixed an issue where the operator would crash when `securityContext.readOnlyRootFilesystem=true` was set in the helm chart values. The operator now creates an emptyDir volume for the webhook certificate.
