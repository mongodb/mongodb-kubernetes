---
kind: other
date: 2026-11-09
---

* **MongoDBSearch**: The default `mongot` container image is now pulled from `quay.io/mongodb/mongodb-enterprise-search` instead of `quay.io/mongodb/mongodb-search`. Mongot team will continue publishing images to the legacy repo until mongot 2.0 for backwards compatibility. The Helm value `search.name` and the environment variable `MDB_SEARCH_NAME` still control the image name.
