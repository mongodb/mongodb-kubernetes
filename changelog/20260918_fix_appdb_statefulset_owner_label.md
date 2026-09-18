---
kind: fix
date: 2026-09-18
---

* **AppDB**: Application Database StatefulSet ownership is now arbitrated with the resource-owner label (`mongodb.com/v1.mongodbOpsManagerResourceOwner`, `mongodb.com/v1.mongodbResourceOwner`, `mongodbmulticluster`) instead of an `ownerReference`. A multi-cluster AppDB StatefulSet was previously given a cross-cluster `ownerReference`, which the Kubernetes garbage collector deletes as an orphaned dependent. Single-cluster AppDB StatefulSets now carry the `mongodb.com/v1.mongodbResourceOwner` label of their `MongoDB` resource.
