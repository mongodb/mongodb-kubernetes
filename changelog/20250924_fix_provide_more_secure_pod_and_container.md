---
kind: fix
date: 2025-09-24
---

* To follow the [Pod Security Standards](https://v1-32.docs.kubernetes.io/docs/concepts/security/pod-security-standards/) more secure default pod `securityContext` settings were added.

  Operator deployment `securityContext` settings that have changed:
   - `allowPrivilegeEscalation: false`
   - `capabilities.drop: [ ALL ]`
   - `seccompProfile.type: RuntimeDefault`

  Other workloads:
   - `capabilities.drop: [ ALL ]`
