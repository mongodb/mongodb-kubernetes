---
kind: fix
date: 2026-10-23
---

* **MongoDBSearch**: Updated the default metrics forwarder image to `otel/opentelemetry-collector-contrib:0.163.2` and pinned the default managed load balancer image to `envoyproxy/envoy:v1.37.10` (previously the floating `v1.37-latest` tag) to address known CVEs. The Envoy and metrics forwarder containers now use the operator's image pull policy (`registry.pullPolicy` in the Helm chart), so updated images replace stale cached ones; Deployment overrides can still set `imagePullPolicy`. If you override `search.metricsForwarderImage`, use OpenTelemetry Collector Contrib 0.157.0 or later. Existing Envoy and metrics forwarder Deployments restart once on upgrade.
