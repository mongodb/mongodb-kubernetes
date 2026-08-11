---
kind: fix
date: 2026-08-11
---

* **MongoDBUser**: Fixed a bug in which `connectionString.standardSrv` secrets for clusters without TLS did not include `tls=false`, causing SRV connection strings to assume TLS by default and fail to connect to non-TLS clusters.
