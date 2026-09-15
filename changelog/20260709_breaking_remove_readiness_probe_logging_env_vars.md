---
kind: breaking
date: 2026-07-09
---

* **Operator**: The `LOG_FILE_PATH`, `MDB_WITH_AGENT_FILE_LOGGING`, `READINESS_PROBE_LOGGER_BACKUPS`, `READINESS_PROBE_LOGGER_MAX_SIZE`, `READINESS_PROBE_LOGGER_MAX_AGE` and `READINESS_PROBE_LOGGER_COMPRESS` environment variables are no longer propagated by the operator onto managed database workloads. This propagation never took effect (the variables were injected into the `mongod` container while the readiness probe runs in the `mongodb-agent` container), so the readiness probe continues to run with its existing defaults and runtime behaviour is unchanged. Setting these variables directly on a resource's containers via the CR is unaffected.
