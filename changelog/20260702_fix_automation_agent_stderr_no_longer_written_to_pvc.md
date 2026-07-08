---
kind: fix
date: 2026-07-02
---

* **MongoDB**: Fixed an issue where `automation-agent-stderr.log` could grow unboundedly on the persistent volume and fill the PVC. The automation agent's stderr is now streamed directly to the pod's stdout instead of written to a file.
