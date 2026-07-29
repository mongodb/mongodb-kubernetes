---
kind: fix
date: 2026-07-29
---

* **MongoDBOpsManager**: Fixed a bug where the Operator could incorrectly mark the `MongoDBOpsManager` resource as `Failed` with the message "The admin-key secret might be corrupted" when the Ops Manager API briefly returned `401 Unauthorized` from the `markAsBackingDatabase` endpoint while the Application Database was restarting (for example during an Ops Manager upgrade). This transient response is now tolerated and retried. Any other `401` — including genuinely broken credentials, which always fail on the first authenticated API call — is still reported as `Failed`.
