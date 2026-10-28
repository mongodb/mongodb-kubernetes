---
kind: fix
date: 2026-10-28
---

* **MongoDBOpsManager**: Fixed Application Database monitoring when Ops Manager is served over HTTPS with a custom CA (`spec.security.tls.ca`). The Application Database agent now trusts the Ops Manager CA and validates the Ops Manager certificate again, instead of failing to connect when the Application Database has no TLS, or skipping certificate validation when it does.
