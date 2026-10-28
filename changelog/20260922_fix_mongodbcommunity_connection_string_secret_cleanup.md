---
kind: fix
date: 2026-09-22
---

* **MongoDBCommunity**: Fixed an issue where the operator could delete a connection string `Secret` that belongs to another resource. The operator now only removes `Secrets` it manages and reports a clear error if the configured `Secret` belongs to something else.
