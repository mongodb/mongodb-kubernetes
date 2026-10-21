---
kind: feature
date: 2026-10-21
---

* **MongoDB**: The Automation Agent now reads its startup settings from a shared `ConfigMap` rendered from `spec.agent.startupOptions`, instead of assembling command-line flags in the launcher. Setting a custom `AGENT_FLAGS` environment variable in the pod template is no longer supported and fails reconciliation. Migrate those settings to `spec.agent.startupOptions`.
