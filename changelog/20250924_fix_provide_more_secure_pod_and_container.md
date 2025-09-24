---
kind: fix
date: 2025-09-24
---

* To follow the [Pod Security Standards](https://v1-32.docs.kubernetes.io/docs/concepts/security/pod-security-standards/) more secure default pod and container securitContext settings were added. 
  Operator deployment securityContext settings that were added:
   - `allowPrivilegeEscalation: false`
   - `capabilities.drop: [ ALL ]`
   - `seccompProfile.type: RuntimeDefault`
  Other workloads:
   - `capabilities.drop: [ ALL ]` 
