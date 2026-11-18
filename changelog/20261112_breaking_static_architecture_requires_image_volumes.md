---
kind: breaking
date: 2026-11-12
---

* **MongoDB**, **MongoDBMultiCluster**, **MongoDBOpsManager**: Static architecture (Public Preview) now requires Kubernetes 1.35 or later, or OpenShift 4.20 or later, with image volumes enabled. Image volumes are on by default on these versions. This is the first step toward static architecture becoming generally available, which will have the same requirement.
  * Upgrade your cluster before you upgrade the operator if it runs an older Kubernetes version or has image volumes turned off. Otherwise static Pods fail to start and log an error that says the cluster needs image volume support.
  * Upgrading the operator restarts each static Pod once. Static Pods no longer run the `mongodb-agent-operator-utilities` container.
  * Deployments using the default non-static architecture are not affected.
