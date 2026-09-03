---
kind: breaking
date: 2026-09-03
---

* **Multi-namespace installs now use namespaced RBAC**: Installing the operator with `operator.watchNamespace` set to a comma-separated list of namespaces now grants the operator one namespaced Role per watched namespace (plus the operator namespace, which is always included) instead of a single ClusterRole bound by per-namespace RoleBindings. The cluster-wide `namespaces list/watch` permission is only granted when watching all namespaces (`operator.watchNamespace="*"`), which is the only mode in which the operator exercises it; installs watching all namespaces and default single-namespace installs are unchanged (byte-identical render). Operators that must install with no cluster-scoped RBAC at all can additionally disable the telemetry ClusterRole (`operator.telemetry.installClusterRole=false`), the ClusterMongoDBRole ClusterRole (`operator.enableClusterMongoDBRoles=false`), and the webhook ClusterRole (`operator.webhook.registerConfiguration=false`). When upgrading an existing multi-namespace install, re-render and re-apply with `helm upgrade` and delete the orphaned ClusterRoles `<operator-name>` and `<operator-name>-pvc-resize`.
