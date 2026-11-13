---
kind: fix
date: 2026-11-13
---

* **Replica set scale-down**: Fixed an issue where scaling down a `MongoDB` resource without external members could remove two voting members in a single Ops Manager update. The operator now steps the member count down from the deployed automation config instead of the reported status count, and the `MongoDB` resource status reports the published member count.
