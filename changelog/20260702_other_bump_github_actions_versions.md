---
kind: other
date: 2026-07-02
---

* **CI**: Bumped the GitHub Actions used in the project's workflows to their latest major versions and pinned them to full commit SHAs to prevent supply-chain attacks via mutable tags: `actions/checkout` (v4 → v7.0.0), `actions/setup-go` (v5 → v6.5.0), and `actions/setup-python` (v5 → v6.3.0). This is a CI-only change with no functional impact on the operator.
