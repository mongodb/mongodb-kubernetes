---
kind: fix
date: 2026-09-07
---

* **MongoDBOpsManager**: connection strings that use the `mongodb+srv://` scheme, or that omit a port after the host, are now redacted in operator logs. Previously `RedactMongoURI` matched only `mongodb://` URIs containing a port and returned any other form unchanged, leaving the password in cleartext.
