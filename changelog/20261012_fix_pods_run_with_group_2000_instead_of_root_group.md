---
kind: fix
date: 2026-10-12
---

* The operator and the pods it creates, `MongoDBCommunity` replica set members among them, now run with group 2000 (`runAsGroup: 2000`) next to user 2000 and `fsGroup: 2000`. The images have no account for uid 2000, so the processes ran with the root group (gid 0). Files on existing volumes keep their ownership: `fsGroup` was already 2000. This does not change anything when `managedSecurityContext` is true, such as on OpenShift.
