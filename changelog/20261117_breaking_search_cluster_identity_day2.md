---
kind: breaking
date: 2026-11-17
---

* **MongoDBSearch multi-cluster**: The `operator.clusterIdentity.clusterName` Helm value and the `OPERATOR_CLUSTER_NAME` environment variable have been removed. The operator now learns its own cluster's identity from the `mck-member-cluster` ConfigMap rendered by `kubectl mongodb cluster generate-resources --member-cluster <name>`; the operator-per-cluster ("simulated multi-cluster") topology for MongoDBSearch is active when the only registered `MemberCluster` is the operator's own cluster. To migrate from MCK 1.x, follow the standard prepare-then-upgrade multi-cluster flow: before upgrading the operator, apply the `generate-resources` output (with `--member-cluster <name>`) on each cluster and register each cluster with its own operator by applying `kubectl mongodb cluster generate-registration` output to the same cluster. This also makes the topology available to OLM installations, which was impossible in MCK 1.x where the identity was a Helm value.
