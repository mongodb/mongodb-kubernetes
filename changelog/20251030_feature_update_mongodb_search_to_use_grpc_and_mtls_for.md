---
kind: feature
date: 2025-10-30
---

* **MongoDBSearch**: Switch to gRPC and mTLS for internal communication
  Since MCK 1.4 the `mongod` and `mongot` processess communicated using the MongoDB Wire Protocol and used keyfile
  authentication. This release switches that to gRPC with mTLS authentication. gRPC will allow for load-balancing search
  queries against multiple `mongot` processes in the future, and mTLS decouples the internal cluster authentication mode
  and credentials among `mongod` processes from the connection to the `mongot` process. The Operator will automatically
  enable gRPC for existing and new workloads, and will enable mTLS authentication if both Database Server and
  `MongoDBSearch` resource are configured for TLS.
