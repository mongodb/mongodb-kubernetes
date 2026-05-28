---
kind: fix
date: 2026-05-28
---

* Fixed an issue where `automation-agent-stderr.log` was never rotated and could grow unboundedly, exhausting all available PVC disk space. The operator now sets a default maximum size of 50 MB for this file (configurable via the `MDB_LOG_FILE_AUTOMATION_AGENT_STDERR_MAX_SIZE_MB` environment variable on the database pod).
