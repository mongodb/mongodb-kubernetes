---
kind: breaking
date: 2026-09-30
---

* **MongoDBCommunity**: User connection string URIs change: the user's database is now passed as `authSource`, the `authMechanism` parameter is derived for SCRAM deployments, the operator defaults `connectTimeoutMS` and `serverSelectionTimeoutMS` are always included, and parameters are sorted deterministically.
* **MongoDBCommunity**: A user database explicitly set to an empty string is no longer defaulted to `admin` by the reconciler: customers with an empty `db` must set the field to `admin` (or remove it so the CRD default applies), otherwise the generated connection strings carry an empty `authSource`.
