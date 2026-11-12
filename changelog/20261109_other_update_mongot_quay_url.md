---
kind: other
date: 2026-11-09
---

* **MongoDBSearch**: The default `mongot` image now comes from `quay.io/mongodb/mongodb-enterprise-search` instead of `quay.io/mongodb/mongodb-search`. After you upgrade the operator, existing `mongot` pods restart to use the new image. If you copy images to your own registry, copy the new one too. To keep using the old repository (still updated until mongot 2.0), set `search.name` in the Helm values (or `MDB_SEARCH_NAME`) to `mongodb-search`.
