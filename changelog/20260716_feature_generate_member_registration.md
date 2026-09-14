---
kind: feature
date: 2026-07-16
---

* **kubectl-mongodb plugin**: Added the `kubectl mongodb multicluster generate-member-registration` command. It connects to a single member cluster using a kubeconfig context, reads the token of the member ServiceAccount (the one `generate-member-resources` rendered, or a pre-provisioned one passed via `--member-cluster-service-account`; the token Secret is discovered via its `kubernetes.io/service-account.name` annotation and must already exist), and writes a credential Secret (a single-context kubeconfig) and a `MemberCluster` CR referencing it to stdout. If the token Secret has not been populated by Kubernetes yet, the command waits for it (up to one minute). Apply the output to the operator's cluster with `kubectl apply` or commit it to Git for GitOps workflows. Together with `generate-member-resources`, this replaces the imperative `multicluster setup` flow for configuring member clusters.
