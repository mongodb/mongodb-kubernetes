---
kind: other
date: 2026-11-02
---

* **Container images**: The operator, `mongodb-kubernetes-init-ops-manager`, `mongodb-kubernetes-readinessprobe` and `mongodb-kubernetes-operator-version-upgrade-post-start-hook` images are now based on `registry.access.redhat.com/ubi9/ubi-micro` instead of `ubi9/ubi-minimal`. The images no longer contain a package manager (`microdnf`, `rpm`), which reduces their size and attack surface. If you extend these images with your own `Dockerfile`, install additional packages in a separate build stage.
