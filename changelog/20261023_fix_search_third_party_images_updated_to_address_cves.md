---
kind: fix
date: 2026-10-23
---

* **MongoDBSearch**: Updated the default metrics forwarder image to `otel/opentelemetry-collector-contrib:0.163.2` and pinned the default managed load balancer image to `envoyproxy/envoy:v1.37.10` to address known CVEs. If you override `search.metricsForwarderImage`, use OpenTelemetry Collector Contrib 0.157.0 or later.
* **MongoDBSearch**: The Envoy and metrics forwarder containers now follow the `registry.pullPolicy` Helm setting, like the `mongot` container.
