---
kind: feature
date: 2026-10-07
---

* **MongoDB**, **MongoDBMultiCluster**, **MongoDBUser**: Connection strings can now carry additional options supplied through `additionalConnectionStringConfig`, matching the community operator. Options set on the resource apply to every connection string built for it, including the ones the operator uses for Ops Manager backups, and options set on a user override the resource options for that user.
