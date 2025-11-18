---
kind: feature
date: 2025-10-30
---

* **MongoDBSearch**:
  * Switched to gRPC and mTLS for internal communication between mongod and mongot.
    * Since MCK 1.4 the `mongod` and `mongot` processess communicated using the MongoDB Wire Protocol and used keyfile authentication. This release switches that to gRPC with mTLS authentication. gRPC will allow for load-balancing search queries against multiple `mongot` processes in the future, and mTLS decouples the internal cluster authentication mode and credentials among `mongod` processes from the connection to the `mongot` process. The Operator will automatically enable gRPC for existing and new workloads, and will enable mTLS authentication if both Database Server and `MongoDBSearch` resource are configured for TLS.
  * Exposed configuration settings for mongot's prometheus metrics endpoint.
    * By default, if `spec.prometheus` field is not provided then metrics endpoint in mongot is disabled. **This is a breaking change**. Previously the metrics endpoing was always enabled on port 9946.
    * To enable prometheus metrics endpoint specify empty `spec.prometheus:` field. It will enable metrics endpoint on a default port (9946). To change the port, set it in `spec.prometheus.port` field.
  * Simplified MongoDB Search setup: Removed the custom Search Coordinator polyfill (a piece of compatibility code previously needed to add the required permissions), as MongoDB 8.2.0 and later now include the necessary permissions via the built-in searchCoordinator role.
  * Updated the default `mongodb/mongodb-search` image version to 0.55.0. This is the version MCK uses if `.spec.version` is not specified.
  * MongoDB deployments using X509 internal cluster authentication are now supported. Previously MongoDB Search required SCRAM authentication among members of a MongoDB replica set. Note: SCRAM client authentication is still required, this change merely relaxes the requirements on internal cluster authentication.
