---
kind: fix
date: 2026-09-15
---

* **Ops Manager requests**: Fixed an issue where Ops Manager API requests kept running to their own timeouts after a reconcile was cancelled or the Operator shut down. Requests are now cancelled together with the reconcile, freeing the worker to handle other resources.
