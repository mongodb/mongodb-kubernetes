---
kind: fix
date: 2026-08-20
---

* **MongoDBUser**: Fixed the generated connection string secret carrying `authMechanism=SCRAM-SHA-256` for users with `spec.db: "$external"` on resources that enable both SCRAM and X.509.
* **MongoDBUser**: The connection string secret no longer contains an empty `password` key for users with `spec.db: "$external"`, since a certificate or LDAP based identity has no password. The key is omitted rather than written empty.
