---
kind: fix
date: 2026-11-25
---

* **MongoDB, MongoDBMultiCluster and MongoDBOpsManager**: Fixed PersistentVolumeClaim resizes that never finished when the storage provider rounded the new capacity up or expanded the file system only after a pod restart, and resizes interrupted by an operator restart that never restarted the pods. A failed resize now names the PersistentVolumeClaim and the `allowVolumeExpansion` and `operator.enablePVCResize` prerequisites.
