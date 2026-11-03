---
kind: fix
date: 2026-10-29
---

* **MongoDB**, **MongoDBMultiCluster**, **MongoDBUser**: Fixed a bug where the operator ignored the configured Vault database secret path and read database secrets from the operator secret path instead. Database secret data and metadata now use the configured database path.
* **MongoDBMultiCluster**: Fixed a bug where hostname override ConfigMaps used the position of a cluster in `spec.clusterSpecList` instead of its stable mapping index. Reordering the list could give pods hostnames from another cluster.
