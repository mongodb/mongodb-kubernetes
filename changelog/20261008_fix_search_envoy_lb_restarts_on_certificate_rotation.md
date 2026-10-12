---
kind: fix
date: 2026-10-08
---

* **MongoDBSearch**: The operator-managed Envoy load balancer now restarts automatically when its TLS server or client certificate `Secret` changes. In multi-cluster deployments, update member-cluster copies before central copies: only central `Secret` events trigger reconciliation. Existing TLS-enabled Envoy `Deployments` restart once on upgrade; non-TLS `Deployments` are unchanged. CA-only rotation is not covered.
