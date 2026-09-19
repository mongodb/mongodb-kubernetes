---
kind: feature
date: 2026-07-15
---

* **kubectl-mongodb plugin**: Added the `kubectl mongodb multicluster generate-member-resources` command. It renders the RBAC a member cluster needs for multi-cluster operation from the operator Helm chart (embedded in the plugin, so the chart is the single source of truth for RBAC) and writes it to stdout. The command is purely local and non-mutating; apply the output with `kubectl apply` or commit it to Git for GitOps workflows. The generated member RBAC is scoped to the member cluster's watched namespaces, so if the operator's watched namespaces change, the RBAC must be regenerated and re-applied.
