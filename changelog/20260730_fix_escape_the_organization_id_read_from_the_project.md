---
kind: fix
date: 2026-07-30
---

* Escape the organization ID read from the project ConfigMap when building Ops Manager API request paths, preventing request forgery to arbitrary Ops Manager endpoints.
