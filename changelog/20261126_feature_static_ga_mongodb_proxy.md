---
kind: feature
date: 2026-11-26
---

* Added the `static-ga` container architecture. In this architecture `mongod` and `mongos` run as children of the `mongodb-proxy` process in the database container, and the automation agent drives them over a Unix socket instead of a shared process namespace. Select it with the `mongodb.com/v1.architecture: static-ga` annotation or the operator default architecture.
* The `static-ga` architecture needs the `mongodb-proxy` image. Set `mongodbProxy.name` and `mongodbProxy.version` in the Helm chart, or `MDB_MONGODB_PROXY_IMAGE` on the operator. A resource that selects `static-ga` without the image is rejected with a clear status message.
* The `static-ga` architecture needs an Agent build that supports `-mongodProxyAddress`. Set `MDB_STATIC_GA_AGENT_IMAGE` on the operator to choose it.
