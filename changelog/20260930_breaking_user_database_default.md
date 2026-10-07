---
kind: breaking
date: 2026-09-30
---

* **MongoDBUser**: The `db` field is no longer required and defaults to `admin` through the CRD schema when omitted. A database explicitly set to an empty string is no longer defaulted to `admin` by the reconciler: customers with an empty `db` must set the field to `admin` (or remove it so the CRD default applies), otherwise the generated connection strings carry an empty `authSource`.
