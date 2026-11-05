---
kind: feature
date: 2026-10-21
---

* **MongoDB and MongoDBMultiCluster**: Configure Automation Agent startup settings with `spec.agent.startupOptions`. Upgrading restarts the database pods once, so use the release-matched init-database image.
