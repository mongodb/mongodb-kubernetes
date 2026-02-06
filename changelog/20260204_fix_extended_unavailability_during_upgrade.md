---
kind: fix
date: 2026-02-04
---

* **MultiClusterSharded**: Fixed extended unavailability that could happen in some cases during Rolling Restarts. Improved StatefulSet creation and update logic to reduce the impact of Kubernetes API stale reads. Also resolved an issue where mongos StatefulSets were previously recreated simultaneously across all clusters
