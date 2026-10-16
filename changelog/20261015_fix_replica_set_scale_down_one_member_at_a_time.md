---
kind: fix
date: 2026-10-15
---

* **MongoDB (replica sets)**: Fixed an issue where scaling a replica set down could fail with `Cannot add/remove multiple voting members of a replica set at once`, because the operator removed several members in one step.
