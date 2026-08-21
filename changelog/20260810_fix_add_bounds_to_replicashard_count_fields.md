---
kind: fix
date: 2026-08-10
---

* Add Bounds to Replica/Shard Count Fields

This prevents setting an incredibly high count and attempting to allocate huge amounts of memory.
