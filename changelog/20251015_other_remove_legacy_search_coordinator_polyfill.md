---
kind: other
date: 2025-10-29
---

* We have streamlined user setup for MongoDB Search by removing the custom Search Coordinator polyfill (a piece of compatibility code previously needed to add the required permissions). Because MongoDB 8.2.0 is now the minimum supported version, it provides the required permissions natively through the searchCoordinator built-in role.
