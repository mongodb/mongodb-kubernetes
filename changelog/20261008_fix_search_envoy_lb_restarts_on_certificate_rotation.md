---
kind: fix
date: 2026-10-08
---

* **MongoDBSearch**: The operator-managed Envoy load balancer now restarts automatically when its TLS server or client certificate `Secret` is rotated, avoiding a manual restart after certificate renewal. In multi-cluster deployments, update member-cluster copies before the central copies: only central `Secret` changes trigger reconciliation. Rotation uses the `Deployment` rollout strategy, which defaults to `RollingUpdate`. With one replica, the default allows one surge pod and zero unavailable replicas, keeping the old pod until its replacement is available. Custom rollout settings can change this behavior; uninterrupted in-flight requests are not guaranteed. Existing TLS-enabled Envoy `Deployments` roll once when upgrading to this version; non-TLS `Deployments` do not roll solely because of this change.
