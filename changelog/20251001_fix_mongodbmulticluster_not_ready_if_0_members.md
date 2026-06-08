---
kind: fix
date: 2025-10-01
---

* **MongoDBMultiCluster**: Fixed resource stuck in Pending state if any `clusterSpecList` item has 0 members. After the fix, a value of 0 members is handled correctly, similarly to how it's done in the **MongoDB** resource.
