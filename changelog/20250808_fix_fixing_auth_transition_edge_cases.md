---
title: Fixing auth transition edge-cases
kind: fix
date: 2025-08-08
---

* The readinessProbe always returns ready if the agent is in a wait step. This can be problematic during auth transitions as we can have a period where we invalidate one auth while the other is not activated yet and we try to use the not supported one.
