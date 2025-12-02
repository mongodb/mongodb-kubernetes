---
kind: fix
date: 2025-12-02
---

* **MongoDB** Adding missing ownerrefs to ensure proper resource deletion by kubernetes.
* **Single Cluster** Deleting resources created by CRD now only happens on multi-cluster deployments. Single Cluster will solely rely on ownerrefs.
