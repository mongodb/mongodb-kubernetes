---
kind: fix
date: 2026-10-16
---

* **MongoDB (replica sets)**: Fixed an issue where a stale cached copy of the resource could overwrite newer status fields, which could cause a replica set scale-down to remove several members at once and fail.
