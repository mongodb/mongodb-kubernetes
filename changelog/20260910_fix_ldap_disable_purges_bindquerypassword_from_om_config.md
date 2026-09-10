---
kind: fix
date: 2026-09-10
---

* **LDAP**: Fixed a bug where disabling LDAP did not remove the stored `bindQueryPassword` from the Ops Manager automation config. The LDAP block (including the plaintext bind credential) previously persisted indefinitely in OM config, backups, and agent downloads even after the user deleted the Kubernetes secret. Now the block is properly deleted from the deployment on disable.