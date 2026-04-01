---
kind: fix
date: 2026-03-27
---

* **MongoDB**: Added a delay before enabling backup for sharded clusters to avoid race condition between Ops Manager topology discovery and backup enablement. The delay is configurable via the `MDB_BACKUP_START_DELAY_SECONDS` environment variable and defaults to 60 seconds.
