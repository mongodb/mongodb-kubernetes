---
kind: feature
date: 2026-08-18
---

* **kubectl-mongodb plugin**: Added a `--member-cluster-ca` flag to `kubectl mongodb multicluster setup` and `kubectl mongodb multicluster recover`, taking a `<member-cluster-name>=<path-to-pem-file>` pair and repeatable once per cluster. The supplied PEM bundle replaces the `certificate-authority-data` entry that the generated KubeConfig would otherwise take from that cluster's ServiceAccount token secret. Use it when TLS is terminated differently on the network path the Operator takes to reach a member cluster's API server.
