---
kind: fix
date: 2026-10-12
---

* `MongoDBCommunity` members now have a liveness probe on the `mongod` container: a TCP check on the `mongod` port, every 30 seconds, that restarts the container after five minutes without a connection. A startup probe on the same port holds it off for up to ten minutes while the container waits for its configuration. A `mongod` that is still running but no longer accepts connections is restarted; a crash was already handled. Both probes can be changed through `spec.statefulSet`. A probe override in `spec.statefulSet` with a different kind of check, such as `exec`, now replaces the default check instead of being combined with it.
