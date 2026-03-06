---
kind: other
date: 2026-02-09
---

* **Container images**: Merged the `init-database` and `init-appdb` init container images into a single `init-database` image. The `init-appdb` image will no longer be published and does not affect existing deployments.