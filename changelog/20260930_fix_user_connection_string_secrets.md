---
kind: fix
date: 2026-09-30
---

* **MongoDBUser**: Reconciliation now fails and retries when the user's password secret cannot be read, instead of publishing connection string secrets with an empty password.
* **MongoDBUser**: Connection strings for sharded clusters now include the configured mongos port instead of always using the default one, matching the cluster connection string secret.
* **MongoDBCommunity**: The per user connection string secrets no longer contain a `password` field for `$external` users (the field was previously written empty), matching the enterprise behavior.
