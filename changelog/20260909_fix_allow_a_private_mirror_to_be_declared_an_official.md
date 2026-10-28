---
kind: fix
date: 2026-09-09
---

* **MongoDBCommunity**: A private mirror of the official MongoDB images can now be declared official via the `MDB_ADDITIONAL_OFFICIAL_MONGODB_REPO_URLS` environment variable, a comma-separated list of registry URLs. Previously the image type suffix (`MDB_COMMUNITY_IMAGE_TYPE`, for example `ubi8`) was appended only for `docker.io/mongodb` and `quay.io/mongodb`, so pointing `MDB_COMMUNITY_REPO_URL` at a mirror silently produced a tag without the suffix — for example `mongodb-community-server:8.0.0` instead of `mongodb-community-server:8.0.0-ubi8` — which does not exist in the mirror or upstream, leaving the StatefulSet in `ImagePullBackOff`. The variable is empty by default, so a repository URL that is neither official nor listed keeps the existing behaviour of taking the version as the whole tag.
