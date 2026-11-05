---
kind: fix
date: 2026-10-15
---

* **MongoDBOpsManager**: Fixed an issue where changing the Application Database password could leave Ops Manager in the `Failed` phase with `The admin-key secret might be corrupted`, because the new password was never applied to the Application Database.
