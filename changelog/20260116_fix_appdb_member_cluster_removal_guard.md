---
kind: fix
date: 2026-01-16
---

* **MongoDBOpsManager**, **AppDB**: Block removing a member cluster while it still has non-zero members. This prevents scaling down without the preserved configuration and avoids unexpected issues.
