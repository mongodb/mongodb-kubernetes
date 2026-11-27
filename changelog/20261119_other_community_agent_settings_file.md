---
kind: other
date: 2026-11-19
---

* **MongoDBCommunity**: The MongoDB Agent now reads its startup settings from a settings file stored in a ConfigMap named `<resource name>-agent-settings`. Upgrading the operator restarts the MongoDBCommunity Pods once.
