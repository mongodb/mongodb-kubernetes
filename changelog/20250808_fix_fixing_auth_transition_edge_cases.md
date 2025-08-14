---
title: Fixing auth transition edge-cases
kind: fix
date: 2025-08-08
---

* Fixed an issue where the readiness probe reported the node as ready even when its authentication mechanism was not in sync with the other nodes, potentially causing premature restarts.
