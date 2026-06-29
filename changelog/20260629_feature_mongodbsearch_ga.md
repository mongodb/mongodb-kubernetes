---
kind: feature
date: 2026-06-29
---

* **MongoDBSearch** is now generally available (GA). This release graduates MongoDB Search from preview to GA. In addition to the schema changes listed under Breaking Changes, it introduces the following capabilities:
  * Updated the default `mongodb/mongodb-search` image version to `1.70.1`. This is the version of MongoDB Search the operator uses if `spec.version` is not specified in the `MongoDBSearch` resource.
  * Added support for MongoDB Search against sharded MongoDB deployments.
  * Added support for forwarding MongoDB Search (mongot) metrics to Ops Manager via the new `spec.observability.metricsForwarder` field, including `spec.observability.metricsForwarder.opsManager` for project and agent credentials. The forwarder status is surfaced under `status.metricsForwarder`.
  * Added support for a password-encrypted private key on the gRPC connection between `mongod`/`mongos` and `mongot` through the new `spec.security.tls.keyFilePasswordSecretRef` field, which references a Secret holding the password that decrypts the password-encrypted key. Omit the field when the key is not encrypted.
  * Added support for client TLS with a password-encrypted key on the SCRAM sync-source connection. Introduced `spec.source.tls.clientCertificateSecretRef` for the client certificate mongot presents during the TLS handshake, and `spec.source.tls.keyFilePasswordSecretRef` to reference a Secret holding the password that decrypts a password-encrypted client key.
  * Added `spec.source.x509.keyFilePasswordSecretRef` to read the password that decrypts a password-encrypted x509 client key from a dedicated Secret. Previously this password had to be embedded as a `tls.keyFilePassword` entry inside the x509 client-certificate Secret; that embedded entry is no longer used.
  * Added `spec.clusters[].advancedMongotConfigs` to pass extra tuning configuration through to the underlying mongot process.
  * Added `spec.clusters[].syncSourceSelector.matchTagSets` to control which replica set members mongot reads from, mapped to mongot's `replicationReader.tagSets`.
  * Added a configurable Envoy retry policy through `spec.clusters[].loadBalancer.managed.retryPolicy` (`numRetries` and `perTryTimeout`). When not set, retries are enabled with sensible defaults (2 retries, 60s per-try timeout).
  * Added `spec.clusters[].loadBalancer.managed.minMongotReadyReplicas` to set the minimum number of ready mongot replicas in a group before the Envoy load balancer routes real traffic to it. Defaults to 1.
  * Added `spec.clusters[].loadBalancer.managed.replicas` to configure the number of Envoy proxy pods deployed for the managed load balancer. Defaults to 1.
  * Added `spec.featureFlags.enableOverloadRetrySignal` to enable or disable the mongot `OVERLOAD_RETRY_SIGNAL` feature flag. Defaults to true.
