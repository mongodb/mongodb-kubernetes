---
kind: breaking
date: 2026-07-22
---

* **Multi-cluster**: Configuring a multi-cluster deployment is now a day-2 operation rather than part of installation. Install the operator as usual (single-cluster baseline), then for each member cluster apply the RBAC from `kubectl mongodb multicluster generate-member-resources` and register it with `kubectl mongodb multicluster generate-member-registration`, which produces a per-cluster credential Secret and a `MemberCluster` CR. The operator discovers member clusters from these `MemberCluster` CRs. The imperative `kubectl mongodb multicluster setup` and `recover` subcommands, the monolithic multi-cluster kubeconfig Secret, and the `mongodb-kubernetes-operator-member-list` ConfigMap are superseded by this flow and will be removed. To recover or re-add a member cluster there is no longer a dedicated command: re-apply the generated RBAC and registration; to remove one, delete its `MemberCluster` CR and credential Secret.
