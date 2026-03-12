---
kind: feature
date: 2026-03-06
---

* **MongoDBCommunity**: Added support for custom resources in `MongoDBCommunity` by introducing a new field `spec.resources` with four subfields: `memberResources`, `memberAgentResources`, `arbiterResources`, and `arbiterAgentResources`. These fields allow users to specify custom resource requests and limits for individual containers (mongod and mongodb-agent) in both member and arbiter pods.
