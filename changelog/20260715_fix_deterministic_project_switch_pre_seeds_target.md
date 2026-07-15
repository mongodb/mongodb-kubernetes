---
kind: fix
date: 2026-07-15
---

* **MongoDB**: Fixed a bug where switching an enterprise `MongoDB` ReplicaSet or ShardedCluster to a new Ops Manager project could cause the automation agent to generate a random keyfile in the empty target project, breaking internal cluster authentication. The operator now copies the full Automation Config (including `auth.key`) from the prior project to the target project before any pod is restarted, preserving the target project's AC version and the existing keyfile contents.
