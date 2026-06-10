# KUBE-38: Search TLS-enable availability — implementation plan

**Design:** [docs/designs/2026-06-08-kube-38-tls-enable-availability.md](../designs/2026-06-08-kube-38-tls-enable-availability.md)

**Goal:** Land a stacked pair — non-TLS bootstrap harness (base) + off→on TLS-enable availability suite
(test, with cert rotation folded in) — under KUBE-38, off KUBE-40.

TDD here is e2e-only (the test framework is not unit-tested): per task, author + static gate
(py_compile/black/isort/flake8, 120-col); the live marker run is the real GREEN.

## Task 0 — Feasibility probe (live) ✔

On the running EVG-host cluster, reuse a deployed search RS to: confirm non-TLS search serves; observe
the straight off→on flip; test the preferTLS hypothesis. Outcome: enable is an inherent bounded outage
(~30–110s, sequencing-dependent), no ride-through; decides the suite assertion shape.

## Task 1 — Non-TLS bootstrap harness (base PR) ✔

**Files:** `tests/common/search/bootstrap_test_mixins.py`, `tests/common/search/search_deployment_helper.py`

- `SearchDeploymentConfig.search_tls: bool = True`.
- `search_set_parameters(tls_mode="requireTLS")` helper; `mdbs_for_ext_rs_source(set_search_tls=True)`.
- Gate the source mongod `searchTLSMode`, the CR `spec.security.tls`, and the LB/search cert steps on
  `search_tls`. Default `True` = unchanged behaviour for existing suites.

## Task 2 — Non-TLS smoke (base PR) ✔

**Files:** `tests/search/search_availability_nontls_smoke.py`, `.evergreen-tasks.yml`, `.evergreen.yml`,
`tests/search/README.md`

Bootstrap `search_tls=False`; assert CR has no `spec.security.tls`, mongod `searchTLSMode=disabled`, and
a clean steady-state window on both query types. Marker `e2e_search_availability_nontls_smoke`, wired
into `e2e_mdb_kind_search_large_task_group`.

## Task 3 — TLS-enable + rotation suite (test PR) ✔

**Files:** `tests/search/search_availability_tls_enable.py`, `.evergreen-tasks.yml`, `.evergreen.yml`,
`tests/search/README.md`

Bootstrap non-TLS → enable TLS under load (assert rolls + `requireTLS` + recovery + post-recovery
progress) → rotate cert (ride-through). Marker `e2e_search_availability_tls_enable`. Retire the
config-change + standalone rotation suites (not carried onto this stack).

## Task 4 — Live GREEN + EVG patch

Run both markers locally green, then one definitive EVG patch (unit + lint + the search e2e tasks).
Stacked draft PRs stay draft.
