---
kind: feature
date: 2026-05-13
---

* **MongoDBOpsManager**, **AppDB**: The `mongodb-agent-monitoring` sidecar container has been merged into the main `mongodb-agent` container. Both automation and monitoring now run as a single process in a single container. The `spec.appDB.monitoringAgent` field is now deprecated and has no effect; a webhook admission warning is emitted when it is set, the field is marked as deprecated in the CRD schema, and a warning is logged at runtime. Agent options for the AppDB — including log level and rotation — are configured via `spec.appDB.agent`, the same field used by `MongoDB` resources.
