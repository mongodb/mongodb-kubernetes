---
kind: feature
date: 2026-07-16
---

* **kubectl-mongodb plugin**: Added the `kubectl mongodb multicluster generate-member-registration` command. It connects to a single member cluster using a kubeconfig context, reads the ServiceAccount token that `generate-member-resources` created on it, and writes a credential Secret (a single-context kubeconfig) and a `MemberCluster` CR referencing it to stdout. Apply the output to the operator's cluster with `kubectl apply` or commit it to Git for GitOps workflows. Together with `generate-member-resources`, this replaces the imperative `multicluster setup` flow for configuring member clusters.
