---
kind: fix
date: 2026-07-29
---

* **MongoDBOpsManager**: Fixed a bug where the Operator could incorrectly mark the `MongoDBOpsManager` resource as `Failed` with the message "The admin-key secret might be corrupted" during an Ops Manager upgrade. While the Application Database restarts, the Ops Manager API can briefly return `401 Unauthorized`; this transient response is now tolerated and retried like any other temporary API error. The `Failed` status is still reported when authentication genuinely fails during initial setup.
