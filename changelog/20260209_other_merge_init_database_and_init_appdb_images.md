---
kind: other
date: 2026-02-09
---

* **Container images**: Merged the `init-database` and `init-appdb` init container images into a single `init-database` image. The `init-appdb` image will no longer be published and does not affect existing deployments.
  * The following Helm chart values have been removed: `initAppDb.name`, `initAppDb.version`, and `registry.initAppDb`. Use `initDatabase.name`, `initDatabase.version`, and `registry.initDatabase` instead.
  * The following environment variables have been removed: `INIT_APPDB_IMAGE_REPOSITORY` and `INIT_APPDB_VERSION`. Use `INIT_DATABASE_IMAGE_REPOSITORY` and `INIT_DATABASE_VERSION` instead.