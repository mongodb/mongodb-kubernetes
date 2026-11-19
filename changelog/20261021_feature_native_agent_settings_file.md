---
kind: feature
date: 2026-10-21
---

* **MongoDB and MongoDBMultiCluster**: Configure Automation Agent startup settings with `spec.agent.startupOptions`. Upgrading restarts the database pods once, so use the release-matched init-database image.
* **MongoDB and MongoDBMultiCluster**: Setting `AGENT_FLAGS`, `BASE_URL`, `GROUP_ID`, `LOG_LEVEL`, `MULTI_CLUSTER_MODE`, `SSL_REQUIRE_VALID_MMS_CERTIFICATES` or `SSL_TRUSTED_MMS_SERVER_CERTIFICATE` in a custom pod template no longer changes the Automation Agent settings. Before upgrading, move these settings to `spec.agent.startupOptions`, for example `logLevel`, `tlsRequireValidMMSServerCertificates` or `httpsCAFile`.
