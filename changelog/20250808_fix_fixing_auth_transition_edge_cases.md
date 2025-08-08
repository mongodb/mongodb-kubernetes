---
title: Fixing auth transition edge-cases
kind: fix
date: 2025-08-08
---

* The agent returns ready if the cluster is ready to accept requests. The operator uses this information to continue operational actions like restarts.
* This can be problematic during auth transitions. We can have a period where we invalidate one auth while the other is not activated yet and we try to use the not supported one
