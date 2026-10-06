---
kind: breaking
date: 2026-09-30
---

* **MongoDBCommunity**: Parameters supplied through `additionalConnectionStringConfig` or user options now override the ones derived from the resource instead of being filtered. Values that were previously dropped, such as `replicaSet`, `ssl` or `tls`, now take effect in the generated connection strings.
