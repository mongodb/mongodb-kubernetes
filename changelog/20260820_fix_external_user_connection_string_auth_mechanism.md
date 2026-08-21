---
kind: fix
date: 2026-08-20
---

* **MongoDBUser**: Fixed the generated connection string secret carrying a SCRAM `authMechanism` (for example `SCRAM-SHA-256`) for users with `spec.db: "$external"` on deployments that enable SCRAM alongside external authentication. The secret now omits `authMechanism`; clients supply the appropriate external mechanism at connect time.
* **MongoDBUser**: The connection string secret no longer contains an empty `password` key for users with `spec.db: "$external"`. Certificate and LDAP based identities have no password; the key is omitted rather than written empty.
