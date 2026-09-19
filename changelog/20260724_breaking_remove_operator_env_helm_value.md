---
kind: breaking
date: 2026-07-24
---

* Remove Operator Env Helm Value

This Helm value (and associated env var `OPERATOR_ENV`) affects default timeouts, logging level/format, and bind addresses for the operator process.
It defaults to `prod`, but `dev` and undocumented `local` also exist, mainly for internal/dev use.
There is no reason to expose this externally as a Helm value - we can instead set it for internal use via the env var.
