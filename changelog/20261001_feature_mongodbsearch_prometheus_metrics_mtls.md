---
kind: feature
date: 2026-10-01
---

* **MongoDBSearch Prometheus metrics mTLS**: Added `spec.observability.prometheus.tls` to opt in to mutual TLS on the mongot `/metrics` endpoint, and configured the built-in metrics forwarder to scrape it over HTTPS with a client certificate. When the option is absent, the endpoint keeps serving plain HTTP. Note: deployments using a `keyFilePasswordSecretRef` see a one-time mongot rolling restart when upgrading the operator (the rotation-hash input was unified).
