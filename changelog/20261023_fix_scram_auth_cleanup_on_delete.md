---
kind: fix
date: 2026-10-23
---

* **MongoDB**, **MongoDBMultiCluster**: Fixed an issue where deleting a resource could leave its deployment and authentication settings in Ops Manager, because a reconcile in flight recreated them after cleanup ran. Cleanup now runs from a finalizer and retries on failure. The resource stays `Terminating` until cleanup finishes, so its name cannot be reused before then. If cleanup keeps failing, for example because Ops Manager rejects the API key, remove the finalizer to finish the deletion without cleanup: `kubectl patch mdb <name> --type=merge -p '{"metadata":{"finalizers":null}}'`. You can also set `mongodb.com/disable-reconciliation: "true"`.
