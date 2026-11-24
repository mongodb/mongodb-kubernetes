---
kind: fix
date: 2026-11-19
---

* **MongoDB and MongoDBMultiCluster**: Fixed an issue where setting `sslRequireValidMMSServerCertificates` to `false` in the project ConfigMap had no effect on the Automation Agent. The agent now follows this setting.
