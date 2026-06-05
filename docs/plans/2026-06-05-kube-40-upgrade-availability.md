# KUBE-40 Upgrade-path availability — Implementation Plan

**Design:** [docs/designs/2026-06-05-kube-40-upgrade-availability.md](../designs/2026-06-05-kube-40-upgrade-availability.md)

**Goal:** Drive a continuous background paging load through five upgrade
paths, count mongot/envoy pod rolls per upgrade, and measure a recovery
time per path; the two operator flavours also emit a roll count
(KUBE-24) and a disruption bound (KUBE-42).

**Architecture:** Pure consumer of the KUBE-37/`#1080` harness. Hybrid
layout — extend `tests/upgrades/operator_upgrade_search.py` for the
operator/chart paths, add `tests/search/search_availability_upgrade_dataplane.py`
for the mongot (local-green) + envoy (EVG-only) paths. Metrics via
structured `logger.info` + bound asserts.

**TDD mode:** e2e-only (repo rule). RED = the new marker not yet
passing / not yet correctly skipping. GREEN = mongot marker passes on
kind; EVG-only scenarios skip cleanly locally and run in the definitive
patch. No unit tests for the test framework.

**EC Context:**
- Decision `search-e2e` #26 — full KUBE-40 design (file layout, local
  vs EVG split, metrics mechanism, must-broaden patch regex).
- Decision `search-e2e` #25 — siblings-off-KUBE-37 stacking.
- Learning `search-e2e` #21 — KUBE-37 impl: verified harness APIs
  (`SearchAvailabilityBackgroundTester` ctor, `wait_for_operations`,
  `assert_no_outage` min_operations floor, `_load_mdbs` via
  `SearchDeploymentHelper.mdbs_for_ext_rs_source`, separate tool per
  tester, `wait_for_sentinel_indexed`).
- Learning `search-e2e` #20 — ride-through + drained-sub-check strategy;
  assert recovery/progress, not zero failures.

**Reference file to mirror:** `tests/search/search_availability_rolling_restart.py`
(helpers `_user_tool`, `_load_mdbs`, `_pod_uids`, `_assert_steady`,
`_assert_rolled_through`; class chain `TestInstallOperator` →
`TestMongoDBDeployment` → `TestSearchDeployment` → `TestSampleData`).

---

## Task 1: Dataplane file scaffold + steady-state gate @tdd

**Files:**
- Create: `docker/mongodb-kubernetes-tests/tests/search/search_availability_upgrade_dataplane.py`

**RED:** marker `e2e_search_availability_upgrade` does not resolve / no
steady-state assertion yet.

**Implement (minimal):** copy the import block, module constants
(`MDB`, `SEARCH`, `MDBS_NAME`, `MONGOT_SELECTOR`, `ENVOY_DEPLOYMENT`,
`ENVOY_SELECTOR`, `BASELINE_OPS`), the shared helpers (`_user_tool`,
`_load_mdbs`, `_pod_uids`, `_assert_steady`), and the deploy chain
classes (`TestInstallOperator`, `TestMongoDBDeployment`,
`TestSearchDeployment`, `TestSampleData`) from
`search_availability_rolling_restart.py` verbatim. Set
`pytestmark = pytest.mark.e2e_search_availability_upgrade` and
`MDB = MongoDBDeploymentConfig(mdb_resource_name="mdb-rs-avail-upgrade")`.
Set `SEARCH = SearchDeploymentConfig()` (defaults
`mongot_replicas=2`, `envoy_lb_replicas=2` = GA config, real
multi-endpoint baseline).

**GREEN:** `_assert_steady` passes after deploy on kind.
```
scripts/dev/wt-ctl attach -- bash scripts/dev/e2e_run.sh e2e_search_availability_upgrade
```
Expected: deploy + steady-state classes PASS (scenarios added next).

**Commit:** `test(search): scaffold upgrade-path availability dataplane suite`

---

## Task 2: Roll-count + recovery-time helpers (KUBE-40-local) @tdd

**Files:**
- Modify: `search_availability_upgrade_dataplane.py`

KUBE-40-local helpers (NOT in `common/search` — keep siblings
independent per design):

```python
def _roll_count(namespace: str, selector: str, before_uids: dict[str, str]) -> int:
    """Pods whose uid changed vs the pre-upgrade snapshot = rolls."""
    after = _pod_uids(namespace, selector)
    rolled = sum(1 for name, uid in after.items() if before_uids.get(name) != uid)
    # account for pods that vanished (replaced under a new name, e.g. Deployment)
    gone = sum(1 for name in before_uids if name not in after)
    return max(rolled, gone)

def _measure_recovery(namespace: str, tool, *, apply_upgrade, wait_ready) -> tuple[float, float]:
    """Returns (recovery_s, disruption_s). Times from upgrade-applied to a
    fresh successful query after pods are Ready. disruption_s approximated
    from the failed-op span in the bg window."""
    ...

def _emit_metric(path: str, *, rolls_mongot: int, rolls_envoy: int, recovery_s: float, disruption_s: float) -> None:
    logger.info(
        f"KUBE40_METRIC path={path} rolls_mongot={rolls_mongot} "
        f"rolls_envoy={rolls_envoy} recovery_s={recovery_s:.1f} disruption_s={disruption_s:.1f}"
    )
```

**RED/GREEN:** exercised by Task 3 (no standalone test — e2e-only).

**Commit:** `test(search): add roll-count + recovery-time helpers`

---

## Task 3: mongot version upgrade scenario (LOCAL-GREEN) @tdd

**Files:**
- Modify: `search_availability_upgrade_dataplane.py`

`TestMongotVersionUpgrade`: steady → bg oneshot+paging window → patch CR
`spec.version` to the upgrade target → wait reconverge (mongot STS rolls)
→ snapshot roll count + recovery → assert post-recovery progress
(`_assert_rolled_through`) + emit metric → `_assert_steady`.

Pick the upgrade target from two operator-known mongot versions
(read current from CR/status; bump to the next available — or parametrise
via an env knob with a sane default). Mongot is a StatefulSet roll, so
mirror `TestMongotRollingRestart`’s drained sub-check for the cursor
fault.

**RED:**
```
scripts/dev/wt-ctl attach -- bash scripts/dev/e2e_run.sh e2e_search_availability_upgrade
```
Expected: FAIL before implementation (scenario asserts not satisfied).

**GREEN:** same command — `TestMongotVersionUpgrade` PASSES on kind; a
`KUBE40_METRIC path=mongot …` line appears in the log.

**Commit:** `test(search): mongot version-upgrade availability scenario`

---

## Task 4: envoy image upgrade scenario (EVG-only) @tdd

**Files:**
- Modify: `search_availability_upgrade_dataplane.py`

`TestEnvoyImageUpgrade`: skip locally (operator out-of-cluster → can't
change `MDB_ENVOY_IMAGE` + restart). Gate the skip on the operator
Deployment absent or `replicas==0`; derive the pod selector from the
Deployment (don't hardcode the Helm label). When it does run (EVG):
change the operator's `MDB_ENVOY_IMAGE`, restart operator, wait envoy
Deployment roll, assert post-recovery progress + emit metric.

```python
import pytest
from tests.conftest import local_operator  # or the Deployment-replicas probe

@pytest.mark.skipif(<operator Deployment absent or replicas==0>,
                    reason="envoy image bump needs an in-cluster operator restart (EVG-only)")
class TestEnvoyImageUpgrade: ...
```

**GREEN (local):** scenario SKIPS cleanly; marker still green overall.

**Commit:** `test(search): envoy image-upgrade scenario (EVG-only skip)`

---

## Task 5: EVG wiring for the dataplane marker @tdd

**Files:**
- Modify: `.evergreen-tasks.yml` (task def `e2e_search_availability_upgrade`)
- Modify: `.evergreen.yml` (add to `e2e_mdb_kind_search_task_group`)

Mirror the KUBE-37 task defs (`e2e_search_availability_rolling_restart`
etc.). Task name == marker. Add in **both** files.

**RED:** `grep e2e_search_availability_upgrade .evergreen*.yml` → only
new entries; `python scripts/evergreen/...validate` (if present) green.

**GREEN:** task resolves in a dry patch / `evergreen validate`.

**Commit:** `chore(evg): wire e2e_search_availability_upgrade task`

---

## Task 6: Extend operator_upgrade_search.py — bg tester + metrics @tdd

**Files:**
- Modify: `docker/mongodb-kubernetes-tests/tests/upgrades/operator_upgrade_search.py`

Purely additive (existing `TestDeployOnOfficialOperator` /
`TestOperatorUpgrade` / `TestScaleWithManagedLB` classes untouched).
Add a module-scoped continuous paging background tester spanning the
upgrade, and wrap the existing `TestOperatorUpgrade.test_upgrade_operator`
with roll-count snapshots + recovery timing + `_emit_metric`. Add new
classes under the **same** marker `e2e_operator_upgrade_search`:

- `TestOperatorUpgradeNoImageBump` — pin `MDB_SEARCH_VERSION` +
  `MDB_ENVOY_IMAGE` across the Helm upgrade (via
  `operator_installation_config` overrides); measure gratuitous rolls;
  emit metric. No hard roll assert yet (report until KUBE-24).
- `TestOperatorUpgradeDefaultImageBump` — let bundled images change;
  assert disruption ≤ a documented bound constant; emit metric.
- `TestChartVersionUpgrade` — Helm chart-version upgrade; measure
  recovery; emit metric.

All EVG-only: `pytest.skip` when the operator runs out-of-cluster
(Deployment absent / `replicas==0`).

**RED/GREEN:** local → all new classes SKIP (operator out-of-cluster);
existing classes unaffected. Real GREEN is the definitive patch.

**Commit:** `test(upgrades): bg availability tester + roll/recovery metrics on operator upgrade`

---

## Task 7: Local green run (mongot) + lint @verifying

```
scripts/dev/wt-ctl attach -- bash scripts/dev/op_run.sh --detach
# delete any stale mdb/mdbs first (one-deploy-per-node CPU limit)
scripts/dev/wt-ctl attach -- bash scripts/dev/e2e_run.sh e2e_search_availability_upgrade
make precommit   # black/isort/flake/yamllint over changed files
```
Expected: `e2e_search_availability_upgrade` — mongot scenario PASS, envoy
scenario SKIP; precommit clean. Confirm a `KUBE40_METRIC path=mongot …`
line in `logs/test-e2e_search_availability_upgrade-*.log`.

---

## Task 8: Changelog (skip) + draft PR @tdd

- Changelog: internal test infra → `skip-changelog` label (KUBE-37
  precedent; `mck-dev:create-changelog` Step-2 classifies as internal).
- PR via `mck-dev:create-pr`: base
  `search/ga-base-KUBE-37-pod-lifecycle`, **draft**, `[skip-ci]` in the
  first 100 chars, self-contained body, `skip-changelog` label.

---

## Task 9: Definitive Evergreen patch @verifying

Broaden the regex to also catch `e2e_operator_upgrade_search` (it does
NOT match `e2e_search_availability_`):

```bash
evergreen patch -p mongodb-kubernetes \
  -v unit_tests,e2e_mdb_kind_ubi_cloudqa_large,e2e_static_mdb_kind_ubi_cloudqa_large \
  --regex_tasks 'lint_repo|^unit_tests$|e2e_search_availability_|e2e_operator_upgrade_search' \
  -d 'search/ga-base-KUBE-40-upgrade-availability: [AI] upgrade-path search availability e2e' -f -y
```
Monitor via `mck-dev:evergreen-analyze-patch`. The EVG-only scenarios
(operator no-bump/default-bump/chart, envoy image) prove out here.

**Patterns to Store (after impl):**
- KUBE-40-local roll-count / recovery-time helper shape (in case KUBE-45
  or a later upgrade ticket wants to promote it to `common/search`).
- Measured disruption bound + roll counts from the first green patch
  (the actual numbers KUBE-24/KUBE-42 cite).
