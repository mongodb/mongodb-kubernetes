# v2 G2 — `e2e_search_q2_mc_rs_steady` execution-0 failure analysis

**Date:** 2026-05-04
**Patch (v2):** https://evergreen.mongodb.com/version/69f83680dd956c000756f522
**Task (spruce):** https://spruce.corp.mongodb.com/task/mongodb_kubernetes_e2e_static_multi_cluster_2_clusters_e2e_search_q2_mc_rs_steady_patch_891db34491348134de423d70b9df9b46d6ffa246_69f83680dd956c000756f522_26_05_04_06_02_42/logs?execution=0
**Branch / HEAD under test:** `mc-search-phase2-q2-rs` @ `94c1c94a8`
**Both executions failed identically (1501s timeout); deterministic — not a flake.**

## Category

**Real bug — test setup defect.** The `mdb` fixture writes search-aware
`--setParameter` keys (`searchIndexManagementHostAndPort`, etc.) into
`spec.additionalMongodConfig.setParameter`, but the resource is then
pinned to a 6.x mongod (via `set_version(ensure_ent_version(custom_mdb_version))`
with the patch's `CUSTOM_MDB_VERSION=6.0.5`). The 6.x mongod doesn't
recognize that parameter, so it refuses to start, the StatefulSet
never reaches Ready, and the MongoDBMulti stays at `Phase.Pending`
until the 1500s wait expires.

## Failure shape

```
Failed: tests.multicluster_search.q2_mc_rs_steady.test_create_mdb_resource (1501s)
Exception: Timeout (1500) reached while waiting for
  MongoDB (mdb-mc-rs-ext-lb)| status: Phase.Pending| message: StatefulSet not ready
File:    tests/multicluster_search/q2_mc_rs_steady.py:328
Line:    mdb.assert_reaches_phase(Phase.Running, timeout=1500)
```

(Test file is on branch `mc-search-phase2-q2-rs`. Line numbers below
reference that branch's HEAD, `94c1c94a8`.)

## Why mongod refused to start

`mdb-mc-rs-ext-lb-0-0` agent log (from artifact
`kind-e2e-cluster-1__a-1777875504-dejwqvxzllz_mdb-mc-rs-ext-lb-0-0-mongodb-agent.log`,
last 50 lines):

```
[2026-05-04T06:50:25.644+0000] [.error] ... Error starting mongod :
{"t":..., "s":"F", "c":"CONTROL", "id":20574, "ctx":"-",
 "msg":"Error during global initialization",
 "attr":{"error":{"code":2,"codeName":"BadValue",
                  "errmsg":"Unknown --setParameter 'searchIndexManagementHostAndPort'"}}}
```

The mongod process never reaches the up state — `LastMongoUpTime: 1970-01-01 00:00:00 +0000 UTC`
in every readiness probe — because the launcher rejects the search
setParameter at startup as "BadValue / Unknown setParameter" before
mongod even opens its listening port.

`searchIndexManagementHostAndPort` (along with `mongotHost` /
`useGrpcForSearch` / `searchTLSMode` / `skipAuthenticationToMongot` /
`skipAuthenticationToSearchIndexManagementServer`) is a setParameter
introduced in the 8.x search-aware mongod build; the 6.x mongod does
not recognize it.

## Why the resource ran on 6.x mongod (not 8.x)

The test calls `set_version(ensure_ent_version(custom_mdb_version))`
in the `mdb` fixture
(`docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py:155`),
where `custom_mdb_version` is the conftest fixture that reads
`CUSTOM_MDB_VERSION` env (default `6.0.7`,
`docker/mongodb-kubernetes-tests/tests/conftest.py:448`). The
v2 patch's CI-injected `CUSTOM_MDB_VERSION=6.0.5`, so the API server
saw `"version":"6.0.5-ent"` (verified directly in the tests-pod log,
`response body: ..."version":"6.0.5-ent"`).

The fixture YAML
(`docker/mongodb-kubernetes-tests/tests/multicluster_search/fixtures/search-q2-mc-rs.yaml`)
omits `spec.version`, so nothing else pins it.

By contrast, every working SC search e2e that writes the same
`setParameter` block (e.g.
`docker/mongodb-kubernetes-tests/tests/search/search_replicaset_external_mongodb_multi_mongot_unmanaged_lb.py`,
which builds via `tests/common/search/search_deployment_helper.py:265`)
loads the `enterprise-replicaset-sample-mflix.yaml` fixture which
**hardcodes `version: 8.2.0-ent`** and **never calls `set_version()`** — so the
search-aware mongod is always used, and the parameter is honored.

## Recommended fix (small, ~5 lines, pattern-matched to working tests)

Mirror the SC search pattern — pin the version in the fixture YAML
and stop overriding it from `CUSTOM_MDB_VERSION`:

1. `docker/mongodb-kubernetes-tests/tests/multicluster_search/fixtures/search-q2-mc-rs.yaml`
   Add under `spec:`:
   ```yaml
     version: 8.2.0-ent
   ```

2. `docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py`
   - Remove line 155: `resource.set_version(ensure_ent_version(custom_mdb_version))`
   - Remove fixture parameter `custom_mdb_version: str` from the `mdb` fixture (line 137)
   - Remove the now-unused `from kubetester.kubetester import ensure_ent_version` (line 48)

That puts q2_mc_rs_steady on the same footing as every other passing
search e2e: `MongoDB(Multi).from_yaml` already declines to clobber a
fixture version that's `>= CUSTOM_MDB_VERSION`
(`docker/mongodb-kubernetes-tests/kubetester/mongodb.py:54-60`,
`semver.compare(get_version(), custom_mdb_version) < 0` is False when
`get_version()` is 8.2.0-ent and CI's CUSTOM_MDB_VERSION is 6.x).

## Why we are NOT pursuing the alternative (defer setParameter via post-Running AC patch)

The fixture's docstring claims the top-level `mongotHost` is needed
to keep "every mongod's startup-time validation happy with
`searchTLSMode=requireTLS`" so that the source RS can reach Running
**before** the per-cluster post-deploy AC patch runs. That claim is
true on 8.x search-aware mongod and false on 6.x — but the right
response is **use 8.x**, not "remove search params from initial AC."
The whole point of the test is to exercise search functionality, which
requires the search-aware mongod. Stripping search params from the
initial AC and re-injecting them later via Ops Manager would still
require an 8.x mongod for any of the search assertions later in the
test to succeed. Pin the version at the fixture; done.

## Recommended next step

1. Push the 3-edit fix above as a NEW commit on branch
   `mc-search-phase2-q2-rs` (do NOT amend `94c1c94a8` — the v2 patch
   is still in flight; this fix lands for the next patch resubmit).
2. Do NOT submit a fresh patch right now per the task's constraint —
   wait for v2 execution-1 to finalize (already failed identically
   per Evergreen REST). Once the user is ready to resubmit, the next
   patch will pick up the fix.
3. After landing the fix, the same diagnosis should be cross-checked
   for the MC sharded q2 test if it's also pending — the same
   `additionalMongodConfig.setParameter` pattern + `set_version`
   anti-pattern may exist there.

## Artifacts

Local copy of the artifacts pulled by `evg task_analyze --execution 0`:
`/Users/anand.singh/.claude/plugins/cache/core-platforms-ai-tools/mck-dev/0.3.8/tmp/evergreen_artifacts/69f83680dd956c000756f522/e2e_static_mult__2_clusters__e2e_search_q2_mc_rs_steady__ex0/`

Key files:
- `artifacts/.../mdb-mc-rs-ext-lb-0-0-mongodb-agent.log` — the smoking gun (`Unknown --setParameter`)
- `artifacts/.../mdb-mc-rs-ext-lb-0-0-pod-describe.txt` — readiness probe failure events
- `artifacts/.../mongodb-enterprise-operator-tests-mongodb-enterprise-operator-tests.log` — pytest output incl. the 1501s timeout traceback and the `"version":"6.0.5-ent"` API request body
