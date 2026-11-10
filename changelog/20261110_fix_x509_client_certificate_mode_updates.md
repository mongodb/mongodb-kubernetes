---
kind: fix
date: 2026-11-10
---

* **MongoDB**, **MongoDBMultiCluster**: Fixed an issue where changes to `spec.security.authentication.requireClientTLSAuthentication` or `spec.security.authentication.modes` were not always applied to deployments using X.509 agent authentication, which could leave the resource in the `Failed` phase. Affected resources recover automatically after the operator is upgraded.
