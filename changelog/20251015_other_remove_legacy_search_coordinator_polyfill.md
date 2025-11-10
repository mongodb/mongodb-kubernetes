---
kind: other
date: 2025-10-15
---

* Simplified MongoDB Search setup: Removed the custom Search Coordinator polyfill (a piece of compatibility code
  previously needed to add the required permissions), as MongoDB 8.2.0 and later now include the necessary permissions
  via the built-in searchCoordinator role.
