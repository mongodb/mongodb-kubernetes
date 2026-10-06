---
kind: fix
date: 2026-09-30
---

* **MongoDBOpsManager**: The connection strings built for S3 backup stores and backup datastores now set `authSource` to the database of the referenced `MongoDBUser` instead of always deriving `admin`, matching the behavior of the user connection string secrets.
