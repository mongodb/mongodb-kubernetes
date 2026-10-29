---
kind: feature
date: 2026-10-21
---

* **MongoDB**: The Automation Agent reads startup settings from a native settings file mounted from a shared `ConfigMap`, including `spec.agent.startupOptions`.
  * **Upgrade migration applies to both static and non-static deployments.** Reconciliation updates the StatefulSet template to use the mounted settings file with `-f`. Replacement pods use this configuration. Existing pods retain their startup configuration until replacement.
  * Static replacement pods receive launcher scripts from the upgraded init-database utilities holder. Their compatible Ops Manager-selected agent image can remain unchanged.
  * Non-static replacement pods receive launcher scripts from the upgraded init-database image, then download the agent before startup.
  * Upgrade the complete installation, including release-matched init-database and non-static database image settings. Pinned older helper images or overridden older launcher shims can prevent startup.
  * The StatefulSet update strategy controls migration timing. `OnDelete` requires pod deletion. A rolling-update partition delays migration for pods below the partition.
  * Custom `AGENT_FLAGS` environment variables in pod templates fail reconciliation. Before upgrading, move those settings to `spec.agent.startupOptions`.
