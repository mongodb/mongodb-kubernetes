---
title: Fix backup agents hostname with external domain
kind: fix
date: 2026-02-03
---

* Replica sets: Fixed an issue where backup agents reported internal cluster hostnames instead of external hostnames when `externalDomain` was configured.
