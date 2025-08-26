---
title: migrate agent repo
kind: feature
date: 2025-08-26
---

* we've migrated the agents to a new repository: `quay.io/mongodb/mongodb-agent`.
  * the agents in the new repository will support x86-64, ARM64, s390x, and ppc64le.
  * operator running >=MCK1.3.0 and static cannot use the agents at `quay.io/mongodb/mongodb-agent-ubi`.
* `quay.io/mongodb/mongodb-agent-ubi` should not be used anymore, it's only there for backwards compatibility.
* More can be read in the [public docs](https://www.mongodb.com/docs/kubernetes/upcoming/tutorial/plan-k8s-op-compatibility/#supported-hardware-architectures)
