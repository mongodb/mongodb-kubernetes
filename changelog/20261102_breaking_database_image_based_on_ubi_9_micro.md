---
kind: breaking
date: 2026-11-02
---

* **Container images**: The `mongodb-kubernetes-database` image, used by the non-static architecture, is now based on `registry.access.redhat.com/ubi9/ubi-micro` instead of `ubi8/ubi-minimal` and no longer contains a package manager (`microdnf`, `rpm`). Because the MongoDB Agent downloads MongoDB builds that match the container's operating system, non-static deployments now require a MongoDB version with RHEL 9 builds: **MongoDB 6.0.3 or later**. Upgrade MongoDB deployments running older versions (for example 4.4 or 5.0) before upgrading the operator, or migrate them to the static architecture.
