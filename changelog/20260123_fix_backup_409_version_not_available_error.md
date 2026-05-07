---
kind: fix
date: 2026-01-23
---

* Fixed an issue where redeploying a MongoDB resource after deletion could fail with 409 "version not available" errors due to stale agent credentials in Ops Manager.

