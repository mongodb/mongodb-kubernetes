---
kind: fix
date: 2026-11-17
---

* **MongoDBSearch**: Updated the default metrics forwarder image to `otel/opentelemetry-collector-contrib:0.165.0` to pick up upstream security and bug fixes. If you override `search.metricsForwarderImage`, use OpenTelemetry Collector Contrib 0.157.0 or later.
