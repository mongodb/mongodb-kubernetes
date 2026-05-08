---
kind: feature
date: 2026-05-08
---

* **MongoDBSearch**: `status.clusterStatusList[i].observedReplicas` now reports the live `Ready` mongot count for each cluster's StatefulSet, surfacing actual scale state alongside the spec'd `replicas` value.
