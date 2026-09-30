---
kind: fix
date: 2026-09-30
---

* **MongoDBUser**: Reconciliation now fails and retries when the user's password secret cannot be read, instead of publishing connection string secrets with an empty password.
* **MongoDBUser**: Connection strings for sharded clusters now include the configured mongos port instead of always using the default one, matching the cluster connection string secret.
* **MongoDBCommunity**: The per user connection string secrets no longer contain an empty `password` field for `$external` users, matching the enterprise behavior.
