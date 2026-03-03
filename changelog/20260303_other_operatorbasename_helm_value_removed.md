---
kind: other
date: 2026-03-03
---

* `operator.baseName` Helm value removed. This value was never intended to be consumed by operator users and was never documented. The value controls the prefix for workload RBAC resource names (`mongodb-kubernetes` default), but changing it could break the operator and workloads because the operator is not aware of custom prefixes. With this change, the Helm chart will no longer allow customisation and the relevant resources will be deployed with predefined names (`ServiceAccount` with names `mongodb-kubernetes-appdb`, `mongodb-kubernetes-database-pods`, `mongodb-kubernetes-ops-manager`, `Role` with name `mongodb-kubernetes-appdb` and `RoleBinding` with name `mongodb-kubernetes-appdb`).
