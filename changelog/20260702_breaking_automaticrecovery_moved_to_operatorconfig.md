---
kind: breaking
date: 2026-07-02
---

* **Operator**: The `MDB_AUTOMATIC_RECOVERY_ENABLE` and `MDB_AUTOMATIC_RECOVERY_BACKOFF_TIME_S` environment variables have been removed. Configure automatic recovery of resources with a broken automation configuration using `.spec.automaticRecovery.mode` (`Enabled`/`Disabled`) and `.spec.automaticRecovery.delay` (back-off in seconds) in the `OperatorConfig` CR instead. The default values remain unchanged (`Enabled` and `1200` seconds).
