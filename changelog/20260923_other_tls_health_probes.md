---
kind: other
date: 2026-09-23
---

* **TLS health probes**: Starting with Ops Manager 9.0, the health endpoint is served exclusively over HTTPS when TLS is enabled, so the `MongoDBOpsManager` readiness, liveness, and startup probes now use HTTPS when TLS is enabled.
