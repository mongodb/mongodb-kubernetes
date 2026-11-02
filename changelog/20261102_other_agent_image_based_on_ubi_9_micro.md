---
kind: other
date: 2026-11-02
---

* **Container images**: Newly built `mongodb-agent` images are based on `registry.access.redhat.com/ubi9/ubi-micro` instead of `ubi9/ubi-minimal` and no longer contain a package manager (`microdnf`, `rpm`). The MongoDB runtime libraries, `nss_wrapper` and the tools used by the agent launcher scripts are still included.
