---
kind: fix
date: 2026-11-19
---

* **MongoDBSearch**: mongot is now always started with the JVM flag `-XX:-UseBASE64Intrinsics`, mitigating CVE-2026-114938. The flag is added before any user-provided `jvmFlags`.
