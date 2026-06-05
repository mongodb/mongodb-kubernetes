# KUBE-40 — Upgrade-path search availability e2e

Parent: KUBE-22. Epic: KUBE-4 (Search GA — Multi-Cluster). Branch:
`search/ga-base-KUBE-40-upgrade-availability`, base
`search/ga-base-KUBE-37-pod-lifecycle`.

## Summary

Drive a continuous background paging load through five upgrade paths,
count mongot/envoy pod rolls before/after each upgrade, and measure a
recovery time per path. The two operator-upgrade flavours additionally
emit a per-run roll count (feeds KUBE-24) and a measured disruption
bound (feeds KUBE-42).

## Goals

- One test per upgrade path, each with a measured recovery time.
- A continuous background availability tester (paging cursor + one-shot)
  spanning each upgrade.
- Per-run roll count (mongot + envoy) for the two operator flavours.
- A measured disruption bound for the default-image-bump flavour that
  KUBE-42 can cite.

## Non-goals

- No change to the operator's upgrade behaviour itself (test-only work).
- No new shared harness module — pure consumer of the KUBE-37/`#1080`
  harness (`background_availability_tester`, `connectivity`,
  `bootstrap_test_mixins`, `search_deployment_helper`).
- Not stacked on KUBE-45; the two are siblings off KUBE-37 (see Decisions
  Log).

## Upgrade paths and where each can run

| Path | Trigger | Local? | Why |
|---|---|---|---|
| operator no-image-bump | Helm-upgrade operator, bundled mongot/envoy images pinned | EVG-only | in-cluster operator upgrade; locally `replicas=0` + out-of-cluster `make run` |
| operator default-image-bump | Helm-upgrade operator, let bundled images change | EVG-only | same |
| mongot version | CR `spec.version` change | **local-green** | out-of-cluster operator reconciles a CR field |
| envoy image | operator env `MDB_ENVOY_IMAGE` | EVG-only | no CR field (`envoyContainerImage()` reads the operator default); needs operator restart |
| MCK chart version | Helm chart upgrade | EVG-only | in-cluster operator upgrade |

Only the mongot-version path proves green on the local kind env; the
other four close via the definitive Evergreen patch.

## Architecture / file layout (hybrid)

### `tests/upgrades/operator_upgrade_search.py` (extend; marker `e2e_operator_upgrade_search`)

Already EVG-wired (`.evergreen-tasks.yml`, two `*_cloudqa_large`
variants) and already drives an operator upgrade with a search workload
via `SampleMoviesSearchHelper` + `SearchTester`. Extend it: deploy once
(its existing module-scoped `mdb`/`mdbs` fixtures), wrap a continuous
paging background tester across the upgrades, then sequentially exercise:

- **no-image-bump** — pin `MDB_SEARCH_VERSION` + `MDB_ENVOY_IMAGE`
  constant across the Helm upgrade; measure gratuitous rolls (expected to
  approach zero once KUBE-24 lands; until then report the count).
- **default-image-bump** — let bundled images change; assert disruption
  fits a documented bound.
- **chart-version upgrade**.

One deploy + sequential upgrades respects the one-full-deploy-per-node
CPU limit. Existing test classes stay untouched (purely additive).

### `tests/search/search_availability_upgrade_dataplane.py` (new; marker `e2e_search_availability_upgrade`)

Mirrors `search_availability_rolling_restart.py` (bootstrap mixins +
`SearchAvailabilityBackgroundTester` + `_user_tool`/`_load_mdbs`/
`_assert_steady`). One deploy, two sequential scenarios:

- **mongot version upgrade** — patch CR `spec.version`; local-green.
- **envoy image upgrade** — change `MDB_ENVOY_IMAGE`; EVG-only.

New EVG task wired in **both** `.evergreen-tasks.yml` (task def) and the
`e2e_mdb_kind_search_task_group` in `.evergreen.yml` (group membership).

## Mechanisms

- **Roll count** — snapshot mongot-STS pods (`app=` from
  `mongot_service_name_for_cluster`) + envoy-Deployment pods (`app=`
  from `lb_deployment_name`) UIDs before each upgrade step; after
  reconverge, count pods whose UID changed. KUBE-40-local helper, not
  shared with KUBE-45 (keep siblings independent).
- **Recovery time** — `time.monotonic()` from upgrade-applied to
  pods-Ready + a fresh successful query. `QueryResult` carries no
  wall-clock timestamp (only internal `elapsed_ms`), so the window is
  timed in-test.
- **Metrics emission** — structured `logger.info("KUBE40_METRIC path=…
  rolls_mongot=… rolls_envoy=… recovery_s=… disruption_s=…")`, matching
  the suite's existing convention (every availability scenario logs
  `verdict.as_dict()`; timings are logged inline). No JSON artifact (no
  test writes metric JSON today). Plus a hard `assert` where a bound is
  enforced (default-image-bump disruption ≤ bound).

## Availability assertions (empirical truths)

- A graceful roll's open-cursor ride-through is timing-dependent —
  assert **post-recovery progress** (succeeded grows past a snapshot
  taken once pods are Ready), not zero failures.
- A drain/outage surfaces in varying error classes — assert on failure
  **count**, not a specific class.

Reuse the `_assert_rolled_through` / `_assert_steady` pattern from
`search_availability_rolling_restart.py`.

## Operator-mode interaction (local vs EVG)

Search e2e forces the operator out-of-cluster locally
(`LOCAL_OPERATOR=true` → `operator.replicas=0`, run via `make run`). Any
path that upgrades the in-cluster operator (no-bump, default-bump,
chart) or restarts it (envoy image) is therefore EVG-only and must
`pytest.skip` locally — gate the skip on the operator Deployment being
absent or `replicas==0`, deriving the pod selector from the Deployment
rather than hard-coding the Helm label.

## Security considerations

Test-only code. No PII, secrets, injection surface, or external
endpoints introduced. No hard-block items apply.

## Decisions Log

- **Stacking — siblings off KUBE-37, not a KUBE-40←KUBE-45 chain
  (revises the brief).** Evidence: the harness both tickets must reuse
  (`background_availability_tester`, `connectivity`,
  `bootstrap_test_mixins`) is missing on `search/ga-base` and present
  only on the KUBE-37 branch, so neither can branch off bare ga-base.
  But KUBE-40 (`tests/upgrades/`) and KUBE-45 (`tests/search/`) touch
  disjoint files with no shared output — exactly the KUBE-36/KUBE-37
  sibling precedent. One level of stacking (onto KUBE-37), no chain.
- **File layout — hybrid.** Extend `operator_upgrade_search.py` for the
  operator/chart paths (reuses its Helm-upgrade harness, honours "extend
  don't rebuild") + one new dataplane file for mongot/envoy. Rejected:
  one-file-one-marker (serial single EVG task, conflicts with
  one-deploy-per-node) and strict per-path five files (re-implements the
  operator-upgrade dance the existing file already has).
- **Metrics — structured log lines + bound asserts.** Matches the
  suite's today-pattern; rejected JSON artifacts (no precedent, needs
  EVG upload wiring).
- **Recovery time measured in-test**, not by extending the harness
  (`QueryResult` has no wall-clock ts).

## Implementation order (feeds writing-plans)

1. New dataplane file — mongot-version scenario (local-green first:
   prove the pattern end-to-end on kind).
2. Dataplane envoy-image scenario (EVG-only; skip locally).
3. Roll-count + recovery-time helpers (KUBE-40-local).
4. Extend `operator_upgrade_search.py` — background tester + metrics
   around the existing upgrade; add no-bump / default-bump / chart
   classes.
5. EVG wiring — new `e2e_search_availability_upgrade` task in both
   `.evergreen-tasks.yml` + `.evergreen.yml`; broaden the definitive
   patch regex to also catch `e2e_operator_upgrade_search`.
6. Local green run of the mongot marker; then the definitive patch.

Definitive patch regex must be broadened — `e2e_operator_upgrade_search`
does **not** match `e2e_search_availability_`:

```
--regex_tasks 'lint_repo|^unit_tests$|e2e_search_availability_|e2e_operator_upgrade_search'
```

Changelog: internal test infra → `skip-changelog` label (KUBE-37
precedent). TDD is e2e-only (no unit tests for the test framework).

## Revision (during execution) — operator-flavor upgrade source

The original design extended `operator_upgrade_search.py` to add
operator-flavor classes upgrading **from the latest released operator**.
Execution surfaced two facts that reshaped this:

- **The managed-LB/envoy feature is unreleased.** The released operator
  has no envoy controller, and `install_official_operator` installs only
  the operator Deployment (not the CRDs) — so managed-LB-before-upgrade
  is impossible on `official -> dev`. The decision: **from = the patch's
  base** (the dev build), not the release.
- **This change is test-only.** The diff vs base touches only `tests/`,
  `docs/`, and `.evergreen*.yml` — no operator Go code, no Helm chart. So
  the base-of-patch operator binary is identical to this build, and
  "from = patch's base" means the **same dev operator on both sides**.
  The operator-version upgrade is then faithfully modelled as a
  **bundled-image change** (`search.version` / `search.envoyImage` Helm
  values) on that one operator — install pinned to older images, then
  Helm-upgrade back to the build defaults so the operator rolls the data
  plane. No separately-built base image is needed or meaningful.

Consequences:
- The operator flavors moved to a **new managed-LB suite**,
  `tests/search/search_availability_upgrade_operator.py` (marker
  `e2e_search_availability_upgrade_operator`, own EVG task → own node, to
  respect the one-deploy-per-node CPU limit). It runs the deploy on a
  managed-LB topology so both mongot and envoy roll counts are meaningful.
- `operator_upgrade_search.py` keeps its released-operator -> dev
  single-mongot upgrade test intact (additive over the parent).
- The **chart-version** flavor is **deferred**: a real chart-version
  upgrade needs a prior released chart carrying the (unreleased) feature,
  which won't exist until GA. Documented in the suite docstring + README
  rather than faked.
- The metric log prefix was renamed `KUBE40_METRIC` -> `SEARCH_UPGRADE_METRIC`
  (code carries no ticket references).

See: docs/plans/2026-06-05-kube-40-upgrade-availability.md
