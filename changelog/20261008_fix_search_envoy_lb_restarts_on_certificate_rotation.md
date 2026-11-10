---
kind: fix
date: 2026-10-08
---

* **MongoDBSearch**: The operator-managed Envoy load balancer now restarts automatically when its TLS server or client certificate `Secret` changes. Existing TLS-enabled Envoy `Deployments` restart once on upgrade; non-TLS `Deployments` are unchanged.
