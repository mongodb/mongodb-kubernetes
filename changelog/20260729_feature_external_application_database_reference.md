---
kind: feature
date: 2026-07-29
---

* **MongoDBOpsManager**: You can now point an Ops Manager resource at an external MongoDB Application Database instead of having MCK deploy the AppDB in-cluster — once AppDB is a standard MongoDB resource it's visible to the same backup, monitoring & lifecycle tooling as the rest of the fleet. MongoDB resources needs to be marked with `spec.role: AppDB` when they serve that role.
