---
kind: feature
date: 2026-07-08
---

* **OpsManager**: The JVM heap percentage for Ops Manager pods is now configurable via the `MDB_OM_JVM_HEAP_PERCENTAGE` environment variable. The default has changed from 90% to 75% (the MongoDB internal standard), reducing the risk of container OOM kills. Users who need the previous value can set `MDB_OM_JVM_HEAP_PERCENTAGE=90`.
* **OpsManager**: Added opt-in support for Ops Manager's native cgroup-aware heap sizing via the `MDB_OM_AUTO_HEAP` environment variable. When enabled, the operator skips setting `-Xmx`/`-Xms` and delegates heap sizing to Ops Manager's built-in logic. This requires OM pods with at least 16Gi of memory.
