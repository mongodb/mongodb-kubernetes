---
title: migrate agent repo
kind: feature
date: 2025-08-26
---

* MongoDB Agent images have been migrated to new container repository: `quay.io/mongodb/mongodb-agent`.
  * the agents in the new repository will support the x86-64, ARM64, s390x, and ppc64le architectures.
  * operator running >=MCK1.3.0 and static cannot use the agent images from the old container repository `quay.io/mongodb/mongodb-agent-ubi`.
* `quay.io/mongodb/mongodb-agent-ubi` should not be used anymore, it's only there for backwards compatibility.
* More can be read in the [public docs](https://www.mongodb.com/docs/kubernetes/upcoming/tutorial/plan-k8s-op-container-images/)
