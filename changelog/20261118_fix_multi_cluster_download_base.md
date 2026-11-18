---
kind: fix
date: 2026-11-18
---

* **MongoDBMultiCluster**: Fixed an issue where `spec.downloadBase` was not applied to the Automation Agent, so a custom download path was ignored and the agent used the default one. Upgrading the operator applies the configured path and restarts the affected database pods once.
