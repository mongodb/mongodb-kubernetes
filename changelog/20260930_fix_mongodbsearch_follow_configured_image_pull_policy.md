---
kind: fix
date: 2026-09-30
---

* **MongoDBSearch**: The `mongot` container now follows the `registry.pullPolicy` Helm setting instead of always pulling its image. Setting it to `IfNotPresent` avoids re-downloading the image every time a search pod restarts.
