---
kind: breaking
date: 2026-07-06
---

* **Operator**: The `MDB_PROPAGATE_PROXY_ENV` environment variable has been removed. Configure propagation of the operator's `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` environment variables onto managed database workloads using `.spec.proxy.envPropagationPolicy` (`Propagate`/`NoPropagation`) in the `OperatorConfig` CR instead. The default behaviour remains unchanged (`NoPropagation`).
