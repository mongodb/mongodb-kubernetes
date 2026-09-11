---
kind: breaking
date: 2026-07-06
---

* **Operator**: The `operator.telemetry.*` Helm values and the `MDB_OPERATOR_TELEMETRY_ENABLED`, `MDB_OPERATOR_TELEMETRY_SEND_ENABLED`, `MDB_OPERATOR_TELEMETRY_COLLECTION_FREQUENCY`, `MDB_OPERATOR_TELEMETRY_COLLECTION_CLUSTERS_ENABLED`, `MDB_OPERATOR_TELEMETRY_COLLECTION_DEPLOYMENTS_ENABLED`, `MDB_OPERATOR_TELEMETRY_COLLECTION_OPERATORS_ENABLED`, `MDB_OPERATOR_TELEMETRY_SEND_FREQUENCY` and `MDB_OPERATOR_TELEMETRY_KUBE_TIMEOUT` environment variables have been removed. Configure telemetry using the `.spec.telemetry` block in the `OperatorConfig` CR instead: `.spec.telemetry.mode` (master switch, `Enabled`/`Disabled`), `.spec.telemetry.collection.frequency`, `.spec.telemetry.collection.kubeTimeout`, `.spec.telemetry.collection.{clusters,deployments,operators}.mode`, `.spec.telemetry.send.mode` and `.spec.telemetry.send.frequency`. Telemetry remains opt-out: absence of any telemetry configuration keeps telemetry enabled, and all default values are unchanged.
