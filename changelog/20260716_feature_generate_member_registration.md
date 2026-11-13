---
kind: feature
date: 2026-07-16
---

* **kubectl-mongodb plugin**: Added the `kubectl mongodb cluster generate-registration` command. It connects to a single member cluster using a kubeconfig context, reads the ServiceAccount token that `generate-resources` created on it, and writes a credential Secret (a single-context kubeconfig) and a `MemberCluster` CR referencing it to stdout. The required `--member-cluster` flag takes the logical name of the member cluster, carried verbatim into the `MemberCluster` CR's `spec.clusterName`; the CR's `metadata.name` and the credential Secret name are derived from it (RFC 1123 compliant names pass through unchanged, others are normalized with a short hash suffix). If the token Secret has not been populated by Kubernetes yet, the command waits for it (up to one minute). Apply the output to the operator's cluster with `kubectl apply` or commit it to Git for GitOps workflows. Together with `generate-resources`, this replaces the imperative `multicluster setup` flow for configuring member clusters.
