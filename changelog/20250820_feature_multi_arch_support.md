---
title: multi-arch support
kind: feature
date: 2025-08-20
---

# Multi-Architecture Support
We've added comprehensive multi-architecture support for the kubernetes operator. This enhancement enables deployment on IBM Power (ppc64le) and IBM Z (s390x) architectures alongside
existing x86_64 support. Core images (operator, agent, init containers, database, readiness probe) now support multiple architectures. We do not add support IBM and ARM support for Ops-Manager and the init-ops-manager image.
