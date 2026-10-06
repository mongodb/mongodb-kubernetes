---
kind: fix
date: 2026-09-30
---

* **MongoDB**: Connection strings for `$external` users keep an explicitly provided `authMechanism` connection parameter instead of dropping it.
