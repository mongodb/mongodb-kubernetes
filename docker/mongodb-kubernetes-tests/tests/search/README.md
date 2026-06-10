# Search e2e tests

End-to-end tests for MongoDB Search (`MongoDBSearch`). Single-cluster suites live
here under `tests/search/`; shared helpers live under `tests/common/search/`.

## Availability tests

Availability tests assert search stays queryable while something happens to the
deployment (steady state, a pod restart, a config change, a connection drain).
They share the harness in `tests/common/search/`:

- `connectivity.py` — `SearchConnectivityTool` (one-shot + paging `$search`, with
  cache-buster queries so a result isn't served stale from mongot's cache) and
  `ConnectivityVerdict`; plus disruption primitives (`delete_pods`,
  `hard_kill_pods_by_label`, `wait_for_mongot_statefulset_drained`,
  `patch_mongot_readiness_probe_to_false`, `paging_baseline_and_fault`).
- `background_availability_tester.py` — `SearchAvailabilityBackgroundTester`
  (background load loop; `wait_for_operations`, `verdict`) plus `assert_no_outage`
  (uninterrupted availability) and `assert_outage_detected` (a fault surfaced).
- `bootstrap_test_mixins.py` — deployment step mixins (`InstallOperatorTests`,
  `MongoDBRsDeploymentTests`, `SearchDeploymentTests`, `SearchSampleDataAndIndexTests`)
  and `SearchE2EFixtures`, configured via `MongoDBRsDeploymentConfig` /
  `SearchDeploymentConfig`.

### Pattern

Compose the deployment mixins, then run the tester:

```python
class TestInstallOperator(InstallOperatorTests): ...
class TestSearchWithReplicaSet(SearchDeploymentTests, MongoDBRsDeploymentTests): ...
class TestSearchSampleDataAndIndex(SearchSampleDataAndIndexTests, SearchE2EFixtures): ...

class TestSteadyStateAvailability(SearchE2EFixtures):
    def test_steady_state_availability(self, mdb):
        tool = SearchConnectivityTool(self._user_tester(mdb))
        with SearchAvailabilityBackgroundTester(tool, mode="paging") as bg:
            bg.wait_for_operations(30)        # steady state; or: trigger a fault here
        assert_no_outage(bg.verdict)          # or assert_outage_detected(...) for faults
```

The example above is the steady-state baseline. A disruption scenario is the same
shape with a real `delete_pods(...)` / drain call inside the `with` block before
the verdict.

### Suites

| Suite | Marker | Scenarios → assertions |
|---|---|---|
| `search_availability_rolling_restart.py` | `e2e_search_availability_rolling_restart` | envoy roll → `assert_no_outage` (oneshot); mongot roll → `other_failed == 0 and cursor_lost == 0` (transient blips allowed while a replica cycles); each plus a drained `paging_baseline_and_fault` sub-check asserting `cursor_lost` |
| `search_availability_envoy_scale.py` | `e2e_search_availability_envoy_scale` | envoy scale up/down via `spec.loadBalancer.managed.replicas` → `assert_no_outage` (oneshot) |
| `search_availability_infra.py` | `e2e_search_availability_infra` | node drain (cordon + evict) → assert failure count, never a specific class; operator restart → `assert_no_outage` plus dataplane pod UIDs unchanged (no spurious roll) |

Assert the oneshot (new-query) verdict: an open paging cursor rides through most
faults on mongod's eager-drain buffer, so background paging windows are
observational; the drained `paging_baseline_and_fault` sub-check is the hard
cursor assertion.

## Conventions

- Markers `e2e_search_*`; single-cluster context `e2e_mdb_kind_ubi_cloudqa`
  (+ om80 / openshift mirrors).
- GA scope is external mongod sources behind the managed Envoy LB.
- Each test is written for a specific Evergreen context — verify the active
  context before running locally.
