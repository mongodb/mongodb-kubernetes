---
kind: fix
date: 2026-03-17
---

* **MongoDBOpsManager**: Correctly handle the edge case where `-admin-key` was created by user and malformed. Previously the error was only presented in DEBUG log entry.
