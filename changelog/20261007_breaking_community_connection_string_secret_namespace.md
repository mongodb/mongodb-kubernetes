---
kind: breaking
date: 2026-10-07
---

* **MongoDBCommunity**: The `connectionStringSecretNamespace` field is removed and the per user connection string secrets are always created in the namespace of the resource. Consumers in another namespace must copy the secret into their own namespace.
