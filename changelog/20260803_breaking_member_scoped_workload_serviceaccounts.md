---
kind: breaking
date: 2026-08-03
---

* **Member-scoped workload ServiceAccounts**: Changed multi-cluster workloads (MongoDB, AppDB, Ops Manager, and mongot pods) on member clusters to run under member-scoped ServiceAccounts `mck-member-appdb`, `mck-member-database-pods`, and `mck-member-ops-manager`, created together with the `mck-member-appdb` Role and RoleBinding by `kubectl mongodb multicluster generate-member-resources`, instead of the fixed `mongodb-kubernetes-*` ServiceAccounts installed by Helm. The generated member-cluster RBAC no longer overlaps with resources managed by the installation tool (Helm/OLM). When upgrading, regenerate and re-apply the member-cluster RBAC and expect a rolling restart of multi-cluster workloads as their pods move to the new ServiceAccounts. Single-cluster deployments are unchanged. Also removed the `operator.createResourcesServiceAccountsAndRoles` and `operator.createOperatorServiceAccount` Helm values: the Helm chart now always deploys RBAC, so the base installation is multi-cluster ready by default.
