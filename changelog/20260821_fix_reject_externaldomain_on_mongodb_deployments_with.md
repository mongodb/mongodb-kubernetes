---
kind: fix
date: 2026-08-21
---

* **MongoDBSearch**: MongoDB resources with `spec.externalAccess.externalDomain` set are now rejected as a Search source; the `MongoDBSearch` resource enters the `Failed` phase with a clear error.
