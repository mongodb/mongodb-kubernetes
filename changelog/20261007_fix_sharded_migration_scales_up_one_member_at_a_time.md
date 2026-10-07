---
kind: fix
date: 2026-10-07
---

* **Sharded migration scaling**: Fixed a bug in the `MongoDB` sharded cluster resource where scaling Kubernetes members up from zero during a VM migration (while `spec.externalMembers` are present) added all new voting members in a single reconfiguration, which Ops Manager rejects. The operator now adds the members one at a time.
