---
kind: breaking
date: 2026-07-01
---

* **Operator**: The `multiCluster.clusterClientTimeout` Helm value and the `CLUSTER_CLIENT_TIMEOUT` environment variable have been removed. Configure the timeout (in seconds) the Operator uses when connecting to a member cluster's Kubernetes API server using `.spec.multiCluster.memberClusterClientTimeout` in the `OperatorConfig` CR instead. The default value remains `10`.
