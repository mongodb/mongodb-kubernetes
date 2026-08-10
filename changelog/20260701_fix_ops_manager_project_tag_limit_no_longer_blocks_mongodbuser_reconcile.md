---
kind: fix
date: 2026-07-01
---

* **MongoDBUser**: Fixed a `401 TOO_MANY_GROUP_TAGS` error caused by the operator tagging the Ops Manager project with the namespace of every MongoDBUser. The user controller no longer adds namespace tags to the project.
