---
kind: breaking
date: 2026-09-30
---

* **MongoDB**, **MongoDBCommunity**: The generated connection strings and secrets state the TLS setting with the canonical `tls` parameter instead of the legacy `ssl` alias (for example `tls=false`), following the driver URI options specification where `ssl` only exists for Atlas compatibility. Consumers parsing the URIs must read `tls`. A user supplied legacy `ssl` parameter is no longer rewritten and will conflict with the derived `tls` value at connect time, use `tls` instead.
