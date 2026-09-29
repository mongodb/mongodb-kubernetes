---
kind: fix
date: 2026-09-29
---

* **MongoDBOpsManager**: Application Database containers now follow the `registry.pullPolicy` Helm setting instead of always pulling their images. Setting it to `IfNotPresent` avoids re-downloading images every time an Application Database pod restarts.
