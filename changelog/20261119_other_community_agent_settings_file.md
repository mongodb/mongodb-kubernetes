---
kind: other
date: 2026-11-19
---

* **MongoDBCommunity**: The MongoDB Agent now reads its startup settings from a settings file stored in a ConfigMap named `<resource name>-agent-settings`. Upgrading the operator restarts the MongoDBCommunity Pods once. It uses `spec.agent.logFile` and `spec.agent.logLevel` exactly as written, so log paths with spaces work and text like `$HOSTNAME` is no longer replaced.
