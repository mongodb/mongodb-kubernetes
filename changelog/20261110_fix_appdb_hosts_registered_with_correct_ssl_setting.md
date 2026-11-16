---
kind: fix
date: 2026-11-10
---

* **MongoDBOpsManager**: Fixed an issue where Application Database hosts showed SSL as disabled in the Ops Manager **All Clusters** view and in Monitoring Settings, even when TLS was enabled for the Application Database. The operator now sends the correct `sslEnabled` value when it registers the Application Database hosts with Ops Manager, and updates already registered hosts on the next reconcile.
