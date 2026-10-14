---
kind: fix
date: 2026-10-08
---

* **MongoDBSearch**: The operator-managed Envoy load balancer now restarts automatically when its TLS server or client certificate `Secret` changes. In hub-and-spoke (centralized multi-cluster) deployments only, update member-cluster copies before central copies: only central `Secret` events trigger reconciliation, and member-cache lag can defer rotation until a later reconciliation. Existing TLS-enabled Envoy `Deployments` restart once on upgrade; non-TLS `Deployments` are unchanged.
