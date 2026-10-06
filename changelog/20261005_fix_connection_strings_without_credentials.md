---
kind: fix
date: 2026-10-05
---

* **MongoDB**: Connection strings without credentials no longer carry the `authSource` and `authMechanism` parameters. MongoDB drivers reject an `authMechanism` that has no username, so the strings published in the `<name>-cluster-connection-string` secret are now accepted as they are.
