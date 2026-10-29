---
kind: fix
date: 2026-10-29
---

* **MongoDBSearch**: An x509-auth `MongoDBSearch` no longer requires the source `MongoDB` to enable a SCRAM authentication mode. An x509-only source now gets the search parameters wired into its configuration instead of being skipped.
