---
kind: feature
date: 2026-11-10
---

* **MongoDBSearch**: The metrics forwarder now reports `mongot` container restarts to Ops Manager. To this end it now runs under a dedicated `mongodb-kubernetes-metrics-forwarder` ServiceAccount that can only `get` and `list` Pods in its own namespace. The Helm chart and `kubectl mongodb multicluster setup` create the ServiceAccount, Role and RoleBinding. If you manage these resources yourself (`operator.createResourcesServiceAccountsAndRoles=false`), create them in every namespace and member cluster where MongoDBSearch runs **before upgrading**; see `public/mongodb-kubernetes.yaml` for the definitions. `kubectl mongodb multicluster setup --source-cluster` copies them from the source cluster and fails if they are missing there, so re-run the setup of the source cluster with this plugin version first.
