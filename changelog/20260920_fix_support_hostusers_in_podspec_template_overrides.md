---
kind: fix
date: 2026-09-20
---

* **Pod template overrides**: Fixed an issue where `hostUsers: false` set in `spec.podSpec.podTemplate` (or `statefulSet.spec.template`) was silently dropped when merged into the operator-managed StatefulSets. User namespaces can now be enabled for database, Ops Manager and AppDB pods on clusters that support them (Kubernetes 1.30+ with the `UserNamespacesSupport` feature).
