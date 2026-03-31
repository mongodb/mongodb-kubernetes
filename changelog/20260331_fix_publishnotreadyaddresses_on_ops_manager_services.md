---
title: Fix publishNotReadyAddresses on Ops Manager services
kind: fix
date: 2026-03-31
---

* Ops Manager and BackupDaemon services no longer set `publishNotReadyAddresses: true`. This previously caused reverse proxies (e.g. Traefik) to route traffic to NotReady pods during rolling upgrades, making Ops Manager temporarily unavailable.
