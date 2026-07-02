---
kind: fix
date: 2026-07-02
---

* **MongoDB**: Fixed an issue where the automation agent's stderr output was written to `automation-agent-stderr.log` on the persistent volume. Under certain failure conditions (for example, when agent logging failed to initialize due to corrupted `.slogger-state-*` files), this file would grow without bound, eventually filling the PVC and crashing the pod. The automation agent's stderr is now streamed directly to the pod's stdout, eliminating unbounded file growth.
