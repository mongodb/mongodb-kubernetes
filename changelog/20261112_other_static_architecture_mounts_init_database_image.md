---
kind: other
date: 2026-11-12
---

* **Static architecture image volumes**: Removed the `mongodb-agent-operator-utilities` container from static architecture (Public Preview) Pods of the `MongoDB`, `MongoDBMultiCluster` and `MongoDBOpsManager` (Application Database) resources. The `mongodb-agent` container now reads the init-database image from a read-only image volume. **Upgrade note:** static deployments now require Kubernetes 1.35 or later (or OpenShift 4.20 or later) with image volumes enabled, which is the default on these versions. Upgrade the cluster before upgrading the operator if it runs an older version or has image volumes turned off. Upgrading the operator restarts each static Pod once. Deployments using the default non-static architecture are not affected.
