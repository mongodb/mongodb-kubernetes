---
kind: fix
date: 2026-09-07
---

* **MongoDB**, **MongoDBMultiCluster**: resources that list `LDAP` in `spec.security.authentication.modes` without a `spec.security.authentication.ldap` block are now rejected with a validation error. Previously such a resource caused a nil pointer dereference that terminated the operator process, stopping reconciliation of every other resource.
