---
kind: fix
date: 2026-03-27
---

* **MongoDB**: Added a 60 seconds delay before enabling backup for sharded clusters to avoid race condition between Ops Manager topology discovery and backup enablement.
