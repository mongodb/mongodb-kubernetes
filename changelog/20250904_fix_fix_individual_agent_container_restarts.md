---
title: Fix individual agent container restarts
kind: fix
date: 2025-09-04
---

* Fixed a bug where the agent container in the static containers architecture would enter a CrashLoopBackOff state if the container restarted.
