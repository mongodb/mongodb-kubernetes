# ST1 MCK Proxy Integration

This document summarizes the approved MCK work for ST1. The shared task
record remains authoritative.

## Scope

Automatically run the Automation Agent and MongoDB in separate containers for
static Enterprise workloads. Cover replica sets, sharded clusters,
multi-cluster deployments, Ops Manager-managed databases, and static AppDB.
Keep non-static workloads unchanged.

Community proxy support, Magic Restore support, process CPU metric
restoration, signed staging images, and public image releases remain deferred.
Do not skip an existing acceptance test to avoid a conflict with a deferral.

## Approved behavior

- In-scope `static-ga` resources use proxy mode automatically.
  Explicit `static` and `non-static` retain their existing paths.
- MCK supplies the matching proxy image. Missing prerequisites or an
  incompatible Agent produce an explicit error, not a legacy fallback.
- An incompatible Agent must not replace healthy existing pods.
- AppDB uses its local Agent configuration and starts or recovers without
  Ops Manager monitoring connectivity.
- Proxy logs go to stdout. MongoDB logs remain in a file and go to stdout
  where feasible.
- Report MongoDB per-process CPU metrics as unavailable in proxy mode.
- Preserve Agent VM behavior and all existing non-static behavior.
- Build only for dev/patch ECR. Image publication, commits, pushes, and PR
  changes require separate authorization.

## Implementation plan

1. Inventory MCK test coverage, image inputs, topology paths, and skips.
   Check Community and Magic Restore applicability before changing acceptance.
2. Register the proxy image in the MCK build and scan flows. Wire automatic
   image selection into static workload construction.
3. Fail reconciliation when static proxy prerequisites or Agent compatibility
   checks fail. Preserve existing healthy pods and legacy behavior outside
   scope.
4. Wire Enterprise replica set, sharded, multi-cluster, Ops Manager-managed,
   and AppDB construction.
5. Add focused regression tests only where existing tests do not cover the
   required behavior.
6. Run targeted Go tests, build checks, and repository precommit checks in
   this worktree. Full Evergreen acceptance and image publication remain
   separate authorized actions.

## Verification status

Evidence date: 2026-11-02. Local checks do not establish live acceptance.

- The isolated branch is `nammn-mck-static-proxy-integration`, based on the
  selected MCK proxy PR head at
  `2fed32564c2d61118b6560a783613355636fa559`.
  The publication stack uses new branches above that base.
  The original integration branch and original PR branch remain unchanged.
  This document is included with the top rollout layer.
- Baseline passed:
  `go test ./controllers/operator/construct ./pkg/mongodbproxy`
- Proxy image build and scan tasks, patch-image metadata, and static task
  registration are implemented. No image build or publication ran.
  The patch metadata fixture now includes the `mongodb-proxy` entry.
  Production metadata and platform lists remain unchanged.
- The operator image helper selects the version-matched dev ECR proxy image,
  honors an explicit image reference, and uses `latest` for development
  builds. `go test ./pkg/images` passed.
- Stop requests now require `main` or `ephemeral`. Supervisor tests cover
  both opposite-role directions with forced zero-timeout stops.
  `go test ./pkg/mongodbproxy ./pkg/images` passed.
- The proxy API now carries optional `operationId` values on start, stop, and
  status. It binds identified executions to role and config, rejects stale
  identified stops, replays the last completed matching operation, and allows a
  new ID after exit. One-shot `waitForExit` starts require an ID. API errors use
  `operation_mismatch`. `go test ./pkg/mongodbproxy` passed after this slice.
- `other.numaPolicy:"auto"` now probes `numactl --show` in the proxy container.
  The proxy interleaves only when the probe succeeds. Existing
  `numaInterleave` behavior remains when the policy is absent. Tests cover
  working and failing probes, missing `numactl`, and explicit false behavior.
- The reported `go fix` JSON-tag change, forbidden `os.Environ` use,
  inconsistent receiver names, and unused Evergreen task are addressed.
- Static-ga test helpers preserve the exact `MDB_DEFAULT_ARCHITECTURE` value,
  assert proxy-backed construction for static-ga, and assert legacy Agent
  launch behavior for non-static. Static-container skips still apply only to
  the legacy `static` value.
- All 26 static test contexts now export `static-ga`. `single_e2e.sh` forwards
  that value to the test chart, which sets `MDB_DEFAULT_ARCHITECTURE` in the
  test pod. MongoDB, TLS, multi-cluster, and Ops Manager AppDB migration tests
  now alternate between static-ga and non-static and assert the resulting
  workload mode.
  The rollout decision remains unresolved. The contexts include ARM,
  POWER, and IBM Z, but immediate implementation and acceptance remain
  amd64-only. Existing context edits remain untouched.
- Holding the context changes alone does not isolate all CI behavior changes.
  The migration helper selects `static-ga` from a `non-static` default.
  Those migration suites also require the compatibility prerequisites.
- Six focused static-ga helper tests passed in earlier checks. Current OLM
  module imports succeed with isolated namespace metadata. No live OLM or
  migration workload ran.
- Enterprise database, sharded, multi-cluster, and AppDB construction paths
  contain static-ga wiring. Construction tests do not establish runtime
  activation across these topologies.
- AppDB proxy startup uses local Agent configuration, writable downloads,
  and mounted binary/tool preparation. Generated-shell tests check
  preparation success and failures before Agent startup.
  They do not execute the real Agent or prove offline startup or recovery.
- OLM upgrade tests explicitly annotate initial resources as legacy `static`
  when the target is `static-ga`. They assert actual pod construction,
  transition after operator upgrade, and check phase, sentinel data, and
  availability through existing test helpers. Non-static coverage remains.
  Local checks do not establish the dynamically selected catalog version
  or prove live upgrade behavior.
- AppDB version upgrade remains unverified. Absence of a proxy startup hook
  does not establish a regression from legacy-static behavior.
  The existing reconciler selects `OnDelete` during version changes and
  can reset it to `RollingUpdate`. That provides another pod-replacement
  path, but its runtime ordering remains unchecked.
- An isolated injected headless readiness test returns ready for an unhealthy
  process expected to be up. The fixture has target config version 6,
  achieved version 5, `LastMongoUpTime=0`, `ReplicationStatus=8`, and an
  incomplete `WaitForCorrectBinaries` step started more than 15 seconds ago.
  The result is `goal=false healthy=false waiting=true ready=true`.
  The fallback in `mongodb-community-operator/cmd/readiness/main.go`
  accepts the old wait step without requiring process health.
- The Agent session's source trace found that failed or stalled reconciliation
  before plan execution can retain a previous wait plan and last-up timestamp.
  MCK readiness health types omit Agent errors, and the headless goal check
  does not inspect them. These findings describe a possible stale-status path,
  not observed post-exit telemetry.
- A normal successful down-state replan should replace `WaitForCorrectBinaries`.
  The injected test does not demonstrate that the Agent produces its fixture
  after a proxy child exits. No real post-exit failure, Pod readiness change,
  or rollout impact is proven.
- Static-ga fails closed when prerequisites or Agent compatibility verification
  are missing. Production callers leave `ProxyAgentCompatibilityVerified`
  false. The permanent compatibility signal remains undecided.
  Image presence, artifact strings, and contract tests do not bypass this gate.
- The Agent session reports five race-enabled real-client/MCK-handler
  contract runs against protocol diff SHA-256
  `680eea87d009134e49662d98e89c25afa5fe8ebf5b30feb0a89f104995383b9e`.
  The harness uses a controlled child, not MongoDB or Linux containers.
  Revalidate this evidence if protocol sources change.
- The mixed Community and Enterprise scale test remains in the shared Cloud QA
  task group and runs on the static variant. Community construction still passes
  `isStatic=false`; the test was not skipped. Its Enterprise path will use the
  compatibility gate when static-ga becomes the variant default.
- The current architecture requirement is amd64 only. Keep the existing scalar
  custom Agent URL. Its filename version must match the scalar `agent_version`
  used as the Agent image tag. No multi-architecture support is claimed.
- `build_info.json` still lists proxy platforms for amd64, arm64, s390x, and
  ppc64le. Compatible non-amd64 Agent artifacts and image manifests remain
  unverified. No URL mapping or Agent build change was made.
- Worktree-local precommit prerequisites exist. The generated Go module cache
  resides outside the source scan, with `GOMODCACHE` pointing to it.
  Cache contents and the real Git index remain preserved.
- `make precommit-full` runs but exits 2. Every hook passes except
  `validate-evergreen-config`. Its exact subprocess is
  `evergreen validate -p mongodb-kubernetes -f .evergreen.yml`.
  The CLI exits 0 but emits this gate-rejected warning:

  ```text
  WARNING: version control is enabled for project 'mongodb-kubernetes' but no project config fields have been set.
  ```

  An extracted clean-base configuration reproduces the warning.
  The clean-base gate also reports its unused proxy e2e task.
  No validator or remote-setting workaround was applied.
  Latest log: `.generated/st1-precommit-metadata-fixture.log`.

### Current focused checks

- Passed:
  `go test -race ./pkg/mongodbproxy ./pkg/images ./pkg/util/architectures ./controllers/operator/construct ./controllers/operator/construct/multicluster ./controllers/operator/inspect ./pkg/statefulset -count=1`.
- Passed:
  `go test ./controllers/operator/construct ./controllers/operator -run 'Test(AppDb|AppDB|MongodbProxy|StaticGAProxy|StaticArchitectureWithoutTheProxy)' -count=1`.
- Passed:
  `python -m pytest scripts/release/tests/build_info_test.py::test_load_build_info_patch -q`
  and `python -m pytest scripts/release/tests/build_info_test.py -q`.
  The selector passes, and all four metadata tests pass.
- Passed: five isolated mocked OLM checks for initial architecture,
  transition scope, test ordering, data/availability orchestration, and
  seeding-failure cleanup. These checks do not contact infrastructure.
- Passed: `uvx ty check tests/olm` from the test project with the existing
  virtual environment active. Black, isort, and the annotation scan pass
  through precommit. `git diff --check` passes.
- No live e2e, cluster changes, Evergreen patch submission, or minimum/maximum
  Kubernetes acceptance ran. AppDB startup, offline recovery, upgrade data,
  and availability remain runtime acceptance gaps.

## Publication stack

The user authorized commits, pushes, and new draft pull requests on new
branches. Local layer branches now exist. Draft publication is pending.

1. `nammn-st1-proxy-contract` targets `KUBE-530/mongodb-proxy` at the recorded base.
   It contains proxy lifecycle, identity, and NUMA changes.
2. `nammn-st1-proxy-packaging` targets `nammn-st1-proxy-contract`.
   It contains dev/patch image packaging and selection.
3. `nammn-st1-static-ga-appdb` targets `nammn-st1-proxy-packaging`.
   It contains controller, AppDB, helper, migration, and OLM changes.
4. `nammn-st1-static-ga-rollout` targets `nammn-st1-static-ga-appdb`.
   It preserves all 26 context edits and this plan.

The protocol layer passes its race-enabled package tests in an exported
committed snapshot. The packaging snapshot passes image-helper tests and
all four metadata tests. Controller construction, architecture, focused
controller tests, and OLM type checks pass after splitting.

All new pull requests remain draft, with live e2e explicitly pending.
The user waived the precommit gate for these drafts only after disclosure
of the clean-base Evergreen warning. The warning remains unresolved.
The user deferred the changelog. Add or decide the changelog before promotion.
The context rollout decision remains unresolved. Publishing its draft
does not establish non-amd64 support or approve rollout.
Do not submit Evergreen patches or publish images.
Preserve both original pull requests unchanged.

## Current status

MCK implementation is in progress. Image-selection, stop-role, operation
identity, NUMA policy, construction, AppDB binary preparation, and metadata
fixtures have focused local evidence. Compatibility, context rollout,
AppDB runtime behavior, and live acceptance remain open.
No non-amd64 support is claimed.

The aggregate tracked source diff before stack commits has SHA-256
`53dd930e4e665be7cda0598515be1294c68acd03032754da110698bc72e12d8d`.
This document is outside that fingerprint.
No image was built or published. The original branch history remains intact.
