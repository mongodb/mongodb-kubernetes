---
kind: fix
date: 2026-02-04
---

* Fixed `Statefulset` update logic that might result in triggering rolling restart in more than one member cluster at a time.
