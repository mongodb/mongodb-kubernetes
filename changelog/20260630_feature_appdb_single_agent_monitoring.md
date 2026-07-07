---
kind: feature
date: 2026-06-30
---

* **MongoDBOpsManager**, **AppDB**: The `mongodb-agent-monitoring` sidecar container has been merged into the main
  `mongodb-agent` container. Both automation and monitoring now run as a single process in a single container. The
  `spec.appDB.monitoringAgent` field is now deprecated and has no effect; Monitoring Agent options for the AppDB —
  including log level and rotation — are configured via `spec.applicationDatabase.agent` and
  `spec.applicationDatabase.agent.monitoringAgent`, the same fields used by `MongoDB` resources. Enabling AppDB
  monitoring still results in rolling restarts of AppDB pods.
