---
kind: feature
date: 2026-07-17
---

* **Multi-cluster**: The operator now discovers member clusters by reading `MemberCluster` CRs and their per-cluster credential Secrets from its own namespace, building one API client per cluster. This replaces the need for the monolithic multi-cluster kubeconfig Secret and the `mongodb-kubernetes-operator-member-list` ConfigMap, which are still honoured as a fallback when no `MemberCluster` CRs are present (removed in a later release). The operator restarts to pick up `MemberCluster` additions, spec changes, and removals; rotating a credential Secret in place currently also requires an operator restart.
