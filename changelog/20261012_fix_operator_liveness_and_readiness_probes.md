---
kind: fix
date: 2026-10-12
---

* The operator pod now has a liveness and a readiness probe. The operator serves `/healthz` and `/readyz` on port 8081, set with `operator.probes.port` in the Helm chart. The pod is ready once the webhook server accepts connections, so admission requests are no longer sent to an operator pod that cannot answer them yet. A stuck operator is restarted. The probe timings can be changed under `operator.probes`, and a probe set to `null` is removed.
