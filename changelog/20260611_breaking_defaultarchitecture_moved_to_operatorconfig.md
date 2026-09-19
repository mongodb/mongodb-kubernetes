---
kind: breaking
date: 2026-06-11
---

* **Operator**: The `operator.mdbDefaultArchitecture` Helm value and the `MDB_DEFAULT_ARCHITECTURE` environment variable have been removed. Configure the default container architecture using `.spec.defaultArchitecture` in the `OperatorConfig` CR instead. The default value remains `NonStatic`. Users who previously set this value must create or update their `OperatorConfig` resource before upgrading to MCK 2.0.
