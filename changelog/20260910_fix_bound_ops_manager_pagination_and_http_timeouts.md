---
kind: fix
date: 2026-09-10
---

* **Ops Manager connectivity**: Fixed an issue where a misconfigured or unresponsive Ops Manager endpoint could stall a reconcile worker indefinitely. The Ops Manager base URL set in the project `ConfigMap` is now validated, and request timeouts are enforced so that reconciliation retries or continues upon failure.
