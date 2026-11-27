---
kind: feature
date: 2026-11-10
---

* **MongoDBSearch**: The metrics forwarder now reports `mongot` container restarts to Ops Manager. To this end it now runs under a dedicated `mongodb-kubernetes-metrics-forwarder` ServiceAccount that can only `get` and `list` Pods in its own namespace. Create this ServiceAccount, its Role and RoleBinding in every namespace and member cluster where MongoDBSearch runs **before upgrading**; otherwise the metrics forwarder pods can't start.
  * On the cluster where the operator is installed, the Helm chart creates them, unless you set `operator.createResourcesServiceAccountsAndRoles=false`.
  * `helm upgrade` doesn't update multi-cluster member clusters. Re-apply the chart's `templates/database-roles.yaml` there, or re-run `kubectl mongodb multicluster setup --install-database-roles` with this plugin version. That command only adds the missing resources and leaves existing ones alone.
  * If you manage these resources yourself, see `public/mongodb-kubernetes.yaml` for the definitions.
  * `kubectl mongodb multicluster recover --source-cluster` copies these resources from the source cluster and fails if they are missing there. Make sure they exist on the source cluster before you run it.
