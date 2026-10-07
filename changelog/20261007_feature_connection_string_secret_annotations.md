---
kind: feature
date: 2026-10-07
---

* **MongoDB**, **MongoDBUser**: The generated connection string secrets can now carry annotations supplied through `connectionStringSecretAnnotations`, matching the community operator. On `MongoDB` the annotations apply to the cluster connection string secret, on `MongoDBUser` to the per user secret.
