---
kind: breaking
date: 2026-06-09
---

* **Operator**: The `operator.maxConcurrentReconciles` Helm value and the `MDB_MAX_CONCURRENT_RECONCILES` environment variable have been removed. Configure the maximum number of concurrent reconciliations per controller using `.spec.maxConcurrentReconciles` in the `OperatorConfig` CR instead. The default value remains `1`. Users who previously set this value must create or update their `OperatorConfig` resource before upgrading to MCK 2.0.
