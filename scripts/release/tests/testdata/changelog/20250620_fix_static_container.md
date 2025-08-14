---
title: Fix for static container architecture
kind: fix
date: 2025-06-20
---

* Fixed a bug where workloads in the `static` container architecture were still downloading binaries. This occurred when the operator was running with the default container architecture set to `non-static`, but the workload was deployed with the `static` architecture using the `mongodb.com/v1.architecture: "static"` annotation.
