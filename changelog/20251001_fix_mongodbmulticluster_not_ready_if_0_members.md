---
kind: fix
date: 2025-10-01
---

* **MongoDBMultiCluster**: fix resource stuck in Pending state if any `clusterSpecList` item has 0 members. After the fix 0 members value is handled correctly similarly how it's done in **MongoDB** resource
