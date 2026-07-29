---
kind: other
date: 2026-07-29
---

* Remove dead `publish_om70_images` and `publish_om80_images` Evergreen build variants and the orphaned `publish_ops_manager` task. These variants were no longer triggered by any automatic or manual path after PCT stopped adding them to bump PR patches in CLOUDP-305848.
