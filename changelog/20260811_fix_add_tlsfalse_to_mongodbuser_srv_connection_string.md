---
kind: fix
date: 2026-08-11
---

* **MongoDBUser**: Include `tls=false` in `connectionString.standardSrv` secrets for clusters without TLS configured, so SRV connection strings can be used directly without manually adding the parameter.
