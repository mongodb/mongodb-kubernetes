---
kind: fix
date: 2026-10-14
---

* **MongoDBMultiCluster**: Fixed reporting of the resource status. Errors returned by Ops Manager are now shown in the resource status, and `status.link` now points to the project in Ops Manager, as it does for `MongoDB`.
