# Plan 10 — Decouple AppDB StatefulSet Construction from MCO

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove `controllers/operator/construct/appdb_construction.go`'s dependency on `mongodb-community-operator/controllers/construct` so AppDB constructs its StatefulSet without any MCO import.

**Architecture:** Two-PR approach. PR 10a is a pure mechanical copy — AppDB gets its own copies of the MCO StatefulSet builder and agent command helpers, with the only code change being replacing MCO constants with already-existing Enterprise equivalents (`util.AgentContainerName`) or new local constants. No logic changes. PR 10b is the cleanup pass: simplify signatures on both sides, remove dead code paths, delete the constants that moved. Both PRs must pass CI independently.

**Tech Stack:** Go 1.21, `controllers/operator/construct` package, `pkg/util/constants.go`, controller-runtime reconciler pattern (`workflow.Status`, `r.updateStatus`).

---

## Background

`appdb_construction.go` calls six symbols from `mongodb-community-operator/controllers/construct`:

| Symbol | Type | Used for |
|--------|------|----------|
| `AgentName` | const `"mongodb-agent"` | container name; `util.AgentContainerName` already has the same value |
| `MongodbName` | const `"mongod"` | container name; no Enterprise equivalent yet |
| `MongoDBAssumeEnterpriseEnv` | const `"MDB_ASSUME_ENTERPRISE"` | env var name; only read by Enterprise |
| `AutomationAgentCommand(...)` | func | builds bash command array for agent |
| `BuildMongoDBReplicaSetStatefulSetModificationFunction(...)` | func | core STS construction; calls private MCO helpers |
| `GetMongodbUserCommandWithAPIKeyExport(...)` | func | bash preamble for API key export |

`appdbreplicaset_controller.go` also imports `mongodb-community-operator/pkg/util/result` for a single `result.OK()` call — replaced with `r.updateStatus(ctx, opsManager, workflow.OK(), log)`.

All transitive imports of the MCO construct package (`pkg/kube/*`, `pkg/statefulset`, `pkg/util/scale`, `pkg/automationconfig`) are already at repo root level — no new cross-boundary imports are introduced by the copy.

The one exception is `collectEnvVars()` which uses MCO's `mongodb-community-operator/pkg/readiness/config` for five string constants. These are copied as local unexported constants (same values, no MCO import).

---

## File Map

### PR 10a

| Action | File | Responsibility |
|--------|------|----------------|
| Modify | `pkg/util/constants.go` | Add `MongodbContainerName = "mongod"` next to `AgentContainerName` |
| Create | `controllers/operator/construct/appdb_statefulset.go` | `AppDBStatefulSetOwner` interface + `BuildMongoDBReplicaSetStatefulSetModificationFunction` (verbatim copy) + all private helpers it calls |
| Create | `controllers/operator/construct/appdb_agent_command.go` | `AutomationAgentCommand`, `GetMongodbUserCommandWithAPIKeyExport`, `BaseAgentCommand`, `MongodbUserCommand` (verbatim copies) + their private constants |
| Modify | `controllers/operator/construct/appdb_construction.go` | Replace 6 `construct.X` references with local/`util.X`; remove MCO import |
| Modify | `controllers/operator/appdbreplicaset_controller.go` | Replace `result.OK()` with `r.updateStatus(ctx, opsManager, workflow.OK(), log)`; remove `pkg/util/result` import |

### PR 10b

| Action | File | Responsibility |
|--------|------|----------------|
| Modify | `controllers/operator/construct/appdb_statefulset.go` | Remove `versionUpgradeHookImage` and `readinessProbeImage` string params (always `""` in AppDB path); use only the `withInitContainers==false` (static) branch |
| Modify | `controllers/operator/construct/appdb_agent_command.go` | Remove `withAgentAPIKeyExport bool` dead param from `AutomationAgentCommand` (AppDB always passes `true`) |
| Modify | `mongodb-community-operator/controllers/construct/mongodbstatefulset.go` | Remove `withInitContainers bool` and `initAppDBImage string` params from `BuildMongoDBReplicaSetStatefulSetModificationFunction`; MCO callers pass `true`/`""` today — inline those; remove `MongoDBAssumeEnterpriseEnv` const; simplify `AutomationAgentCommand` (drop `withAgentAPIKeyExport` branch: community always passes `false`) |
| Modify | `mongodb-community-operator/controllers/construct/build_statefulset_test.go` | Update test calls for simplified signatures |

---

## Chunk 1 — PR 10a: Groundwork (constants + new files)

### Task 1: Add `MongodbContainerName` to `pkg/util/constants.go`

**Files:**
- Modify: `pkg/util/constants.go` (near line 92 where `AgentContainerName` lives)

- [ ] Read `pkg/util/constants.go` around line 90 to find the exact insertion point.

- [ ] Add the constant immediately after `AgentContainerName`:
  ```go
  AgentContainerName  = "mongodb-agent"
  MongodbContainerName = "mongod"
  ```

- [ ] Build to confirm no compile errors:
  ```bash
  cd /path/to/repo && go build ./pkg/util/...
  ```
  Expected: no output (clean build).

- [ ] Commit:
  ```bash
  git add pkg/util/constants.go
  git commit -m "feat(decouple): add MongodbContainerName constant to pkg/util"
  ```

---

### Task 2: Create `appdb_agent_command.go`

**Files:**
- Create: `controllers/operator/construct/appdb_agent_command.go`

The file contains verbatim copies of the four agent command symbols from MCO's `mongodbstatefulset.go`. The only changes are:
- `package construct` (same package as the target directory)
- Replace `config.ReadinessProbeLoggerBackups` etc. with local unexported string constants (same values, no MCO import).

- [ ] Read the source functions in full from MCO:
  ```
  mongodb-community-operator/controllers/construct/mongodbstatefulset.go
  lines 58-70 (MongodbUserCommand, automationAgentOptions, clusterFilePath, agentHealthStatusFilePathValue constants)
  lines 243-283 (BaseAgentCommand, AutomationAgentCommand, GetMongodbUserCommandWithAPIKeyExport)
  ```

- [ ] Create `controllers/operator/construct/appdb_agent_command.go` with `package construct` and the following content:

  ```go
  package construct

  import (
      "strconv"

      v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
  )

  // Private constants — verbatim values from MCO's mongodbstatefulset.go and readiness/config/config.go.
  const (
      appdbClusterFilePath              = "/var/lib/automation/config/cluster-config.json"
      appdbAgentHealthStatusFilePathValue = "/var/log/mongodb-mms-automation/healthstatus/agent-health-status.json"
      appdbAutomationAgentOptions        = " -skipMongoStart -noDaemonize -useLocalMongoDbTools"

      // Readiness probe logger env var names — from MCO's pkg/readiness/config.
      appdbReadinessProbeLoggerBackups  = "READINESS_PROBE_LOGGER_BACKUPS"
      appdbReadinessProbeLoggerMaxSize  = "READINESS_PROBE_LOGGER_MAX_SIZE"
      appdbReadinessProbeLoggerMaxAge   = "READINESS_PROBE_LOGGER_MAX_AGE"
      appdbReadinessProbeLoggerCompress = "READINESS_PROBE_LOGGER_COMPRESS"
      appdbWithAgentFileLogging         = "MDB_WITH_AGENT_FILE_LOGGING"
      appdbAgentHealthStatusFilePathEnv = "AGENT_STATUS_FILEPATH"
  )

  // MongodbUserCommand is the bash preamble that sets up the correct UID mapping for mongod.
  // Verbatim copy from MCO's mongodbstatefulset.go.
  const MongodbUserCommand = `current_uid=$(id -u)` // ... (paste full value)

  // BaseAgentCommand returns the core agent binary invocation flags.
  func BaseAgentCommand() string {
      return "agent/mongodb-agent -healthCheckFilePath=" + appdbAgentHealthStatusFilePathValue + " -serveStatusPort=5000"
  }

  // AutomationAgentCommand returns the full command array for the automation agent container.
  // Verbatim copy from MCO's mongodbstatefulset.go.
  func AutomationAgentCommand(withStatic bool, withAgentAPIKeyExport bool, logLevel v1.LogLevel, logFile string, maxLogFileDurationHours int) []string {
      // ... paste full body
  }

  // GetMongodbUserCommandWithAPIKeyExport returns the bash preamble that exports AGENT_API_KEY from a file.
  // Verbatim copy from MCO's mongodbstatefulset.go.
  func GetMongodbUserCommandWithAPIKeyExport(withStatic bool) string {
      // ... paste full body
  }
  ```

  **Important**: paste the complete, verbatim function bodies. Do not summarise or shorten.

- [ ] Build:
  ```bash
  go build ./controllers/operator/construct/...
  ```
  Expected: clean.

- [ ] Commit:
  ```bash
  git add controllers/operator/construct/appdb_agent_command.go
  git commit -m "feat(decouple): copy MCO agent command helpers into Enterprise construct package"
  ```

---

### Task 3: Create `appdb_statefulset.go`

**Files:**
- Create: `controllers/operator/construct/appdb_statefulset.go`

This is the largest file — a verbatim copy of MCO's `BuildMongoDBReplicaSetStatefulSetModificationFunction` plus all private helpers it calls. Read the full MCO source before writing.

- [ ] Read the following sections of `mongodb-community-operator/controllers/construct/mongodbstatefulset.go` in full:
  - Lines 72–106 (`MongoDBStatefulSetOwner` interface)
  - Lines 108–241 (`BuildMongoDBReplicaSetStatefulSetModificationFunction`)
  - Lines 288–527 (all private helpers: `mongodbAgentContainer`, `mongodbAgentUtilitiesContainer`, `versionUpgradeHookInit`, `dataPvc`, `logsPvc`, `readinessProbeInit`, `mongodbContainer`, `collectEnvVars`)

- [ ] Create `controllers/operator/construct/appdb_statefulset.go`. Rules for the copy:
  - `package construct`
  - Rename interface `MongoDBStatefulSetOwner` → `AppDBStatefulSetOwner` (for clarity; the function signature updates accordingly)
  - `AgentName` → `util.AgentContainerName` (from `pkg/util`)
  - `MongodbName` → `util.MongodbContainerName` (added in Task 1)
  - `mongodbDatabaseServiceAccountName` → `appDBServiceAccount` (already defined in `appdb_construction.go:35` in the same package)
  - `agentHealthStatusFilePathValue` → `appdbAgentHealthStatusFilePathValue` (defined in `appdb_agent_command.go`)
  - `agentHealthStatusFilePathEnv` → `appdbAgentHealthStatusFilePathEnv` (defined in `appdb_agent_command.go`)
  - `config.ReadinessProbeLoggerBackups` → `appdbReadinessProbeLoggerBackups` (defined in `appdb_agent_command.go`)
  - `config.ReadinessProbeLoggerMaxSize` → `appdbReadinessProbeLoggerMaxSize`
  - `config.ReadinessProbeLoggerMaxAge` → `appdbReadinessProbeLoggerMaxAge`
  - `config.ReadinessProbeLoggerCompress` → `appdbReadinessProbeLoggerCompress`
  - `config.WithAgentFileLogging` → `appdbWithAgentFileLogging`
  - All remaining logic, volume setup, container specs, PVC builders: **verbatim** — do not change
  - Imports: use root-level packages only (`pkg/kube/container`, `pkg/kube/podtemplatespec`, `pkg/kube/probes`, `pkg/kube/persistentvolumeclaim`, `pkg/statefulset`, `pkg/util/scale`, `pkg/automationconfig`, `api/v1`, `k8s.io/...`) — no MCO imports

  The function signature in this file:
  ```go
  func BuildMongoDBReplicaSetStatefulSetModificationFunction(
      mdb AppDBStatefulSetOwner,
      scaler scale.ReplicaSetScaler,
      mongodbImage, agentImage, versionUpgradeHookImage, readinessProbeImage string,
      withInitContainers bool,
      initAppDBImage string,
  ) statefulset.Modification {
  ```

- [ ] Build:
  ```bash
  go build ./controllers/operator/construct/...
  ```
  Fix any import or name-resolution errors. Common pitfalls: missing `util.AgentContainerUtilitiesName` (verify it exists in `pkg/util/constants.go`); `v1.LogLevel` and `v1.MongodConfiguration` now in `api/v1` (not MCO's `api/v1`).

- [ ] Commit:
  ```bash
  git add controllers/operator/construct/appdb_statefulset.go
  git commit -m "feat(decouple): copy MCO StatefulSet builder and AppDBStatefulSetOwner interface into Enterprise construct"
  ```

---

### Task 4: Wire up `appdb_construction.go` to use local copies

**Files:**
- Modify: `controllers/operator/construct/appdb_construction.go`

- [ ] Read `controllers/operator/construct/appdb_construction.go` in full to locate all 6 `construct.X` references.

- [ ] Replace each reference:
  | Old | New |
  |-----|-----|
  | `construct.AgentName` | `util.AgentContainerName` |
  | `construct.MongodbName` | `util.MongodbContainerName` |
  | `construct.MongoDBAssumeEnterpriseEnv` | local const `mongoDBAssumeEnterpriseEnv = "MDB_ASSUME_ENTERPRISE"` (add at top of file in the existing `const` block) |
  | `construct.AutomationAgentCommand(...)` | `AutomationAgentCommand(...)` (local, same package) |
  | `construct.BuildMongoDBReplicaSetStatefulSetModificationFunction(...)` | `BuildMongoDBReplicaSetStatefulSetModificationFunction(...)` (local, same package) |
  | `construct.GetMongodbUserCommandWithAPIKeyExport(...)` | `GetMongodbUserCommandWithAPIKeyExport(...)` (local, same package) |

  The call site for `BuildMongoDBReplicaSetStatefulSetModificationFunction` passes `&opsManager.Spec.AppDB` as the owner. Verify that `AppDBSpec` satisfies `AppDBStatefulSetOwner` by checking that it has all the required methods (it should — it was satisfying `MongoDBStatefulSetOwner` before).

- [ ] Remove the `"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/controllers/construct"` import line.

- [ ] Build:
  ```bash
  go build ./controllers/operator/...
  ```
  Expected: clean. If `AppDBSpec` is missing any `AppDBStatefulSetOwner` method, the compiler will tell you — add forwarding methods to `AppDBSpec` or adjust the interface.

- [ ] Run existing tests:
  ```bash
  go test ./controllers/operator/construct/... -count=1 -v 2>&1 | tail -30
  ```
  Expected: all pass.

- [ ] Commit:
  ```bash
  git add controllers/operator/construct/appdb_construction.go
  git commit -m "feat(decouple): wire appdb_construction.go to use local StatefulSet builder (drop MCO construct import)"
  ```

---

### Task 5: Replace `result.OK()` in AppDB controller

**Files:**
- Modify: `controllers/operator/appdbreplicaset_controller.go`

- [ ] Read `controllers/operator/appdbreplicaset_controller.go` around line 551 to understand the `shouldReconcile == false` skip path. The current code:
  ```go
  if !shouldReconcile {
      log.Info("Skipping reconciliation ...")
      return result.OK()
  }
  ```

- [ ] Replace with:
  ```go
  if !shouldReconcile {
      log.Info("Skipping reconciliation ...")
      return r.updateStatus(ctx, opsManager, workflow.OK(), log, appDbStatusOption)
  }
  ```
  `r.updateStatus` signature: `func (r *ReconcileCommonController) updateStatus(ctx, reconciledResource, st workflow.Status, log, ...status.Option) (reconcile.Result, error)` — defined in `common_controller.go:301`. Pass `appDbStatusOption` (declared at line 523 as `status.NewOMPartOption(status.AppDb)`) — every other early-exit in this function that writes a status on the AppDB path passes it; omitting it would leave the AppDB sub-status unwritten.

- [ ] Remove the `"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/result"` import line from the file. If `workflow` package is not already imported, verify it is (it almost certainly is given surrounding code).

- [ ] Build:
  ```bash
  go build ./controllers/operator/...
  ```
  Expected: clean.

- [ ] Run controller tests:
  ```bash
  go test ./controllers/operator/... -count=1 -run TestAppDB 2>&1 | tail -20
  ```

- [ ] Commit:
  ```bash
  git add controllers/operator/appdbreplicaset_controller.go
  git commit -m "fix(decouple): replace result.OK() with r.updateStatus() in AppDB skip path; drop result import"
  ```

---

### Task 6: Full build and test gate for PR 10a

- [ ] Full build:
  ```bash
  go build ./...
  ```
  Expected: zero errors.

- [ ] Run full unit test suite for affected packages:
  ```bash
  go test ./controllers/operator/... ./pkg/util/... -count=1 2>&1 | grep -E "FAIL|ok"
  ```
  Expected: all `ok`, zero `FAIL`.

- [ ] Confirm no remaining MCO construct imports from Enterprise (outside of allowlisted files):
  ```bash
  grep -r '"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/controllers/construct"' \
    --include="*.go" . --exclude-dir=mongodb-community-operator | grep -v "main.go"
  ```
  Expected: no output.

- [ ] Confirm no remaining `pkg/util/result` import from Enterprise:
  ```bash
  grep -r '"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/result"' \
    --include="*.go" . --exclude-dir=mongodb-community-operator
  ```
  Expected: no output.

- [ ] Final commit if any lint fixes applied, then push for PR.

---

## Chunk 2 — PR 10b: Simplify (post-10a, MCO + Enterprise cleanup)

> Start this chunk only after PR 10a is merged (or on a branch stacked on top of 10a).

### Task 7: Simplify Enterprise-side `appdb_statefulset.go`

The AppDB call site passes `versionUpgradeHookImage = ""` and `readinessProbeImage = ""` always. However, `withInitContainers = !architectures.IsRunningStaticArchitecture(opsManager.Annotations)` — it is `true` for non-static (init-container) architecture deployments and `false` for static. **Both branches must be kept in the Enterprise copy.** The only safe simplification is removing the two always-`""` parameters.

**Before simplifying**, confirm by reading `appdb_construction.go:414`.

- [ ] Read `controllers/operator/construct/appdb_construction.go` around line 412–420 to confirm:
  1. `versionUpgradeHookImage` and `readinessProbeImage` are always `""`.
  2. The comment at those lines confirms this is intentional — AppDB overrides the init containers downstream so the `""` image values in the `withInitContainers==true` branch never reach a scheduled pod.

- [ ] If both confirmed:
  - Remove `versionUpgradeHookImage string` and `readinessProbeImage string` from `BuildMongoDBReplicaSetStatefulSetModificationFunction`'s signature in `appdb_statefulset.go`
  - Hard-code `""` at the two internal call sites (`versionUpgradeHookInit(mounts, "")` and `readinessProbeInit(mounts, "")`) — safe because AppDB has always passed `""` here and the downstream override always fires before scheduling
  - Keep the `withInitContainers` branching intact
  - Update the call site in `appdb_construction.go` to drop the two `""` arguments
  - **If the downstream-override guarantee cannot be confirmed**, defer this simplification and leave the two parameters in the signature for a later plan

- [ ] Build + test:
  ```bash
  go build ./controllers/operator/...
  go test ./controllers/operator/construct/... -count=1 2>&1 | grep -E "FAIL|ok"
  ```

- [ ] Simplify `AutomationAgentCommand` in `appdb_agent_command.go`:
  - Verify that AppDB always passes `withAgentAPIKeyExport = true` (Enterprise path), never `false`
  - If confirmed, remove the `withAgentAPIKeyExport` parameter and inline the `true` branch
  - Update call sites in `appdb_construction.go`

- [ ] Commit:
  ```bash
  git commit -m "refactor(decouple): Plan 10b — simplify AppDB statefulset builder; remove unused params"
  ```

---

### Task 8: Simplify MCO-side `mongodbstatefulset.go`

**Files:**
- Modify: `mongodb-community-operator/controllers/construct/mongodbstatefulset.go`
- Modify: `mongodb-community-operator/controllers/construct/build_statefulset_test.go`
- Modify: MCO callers of `BuildMongoDBReplicaSetStatefulSetModificationFunction` inside `mongodb-community-operator/`

- [ ] Find all MCO-internal callers:
  ```bash
  grep -rn "BuildMongoDBReplicaSetStatefulSetModificationFunction" \
    --include="*.go" ./mongodb-community-operator/
  ```

- [ ] Verify that all MCO callers pass `withInitContainers = true` and `initAppDBImage = ""`. If so:
  - Remove `withInitContainers bool` and `initAppDBImage string` parameters
  - Remove the `withInitContainers == false` (static) branch — Community does not run static
  - Update all MCO call sites

- [ ] Delete `MongoDBAssumeEnterpriseEnv` constant from `mongodbstatefulset.go` — only Enterprise reads it, and Enterprise now has its own copy.

- [ ] Simplify `AutomationAgentCommand` in MCO:
  - Verify MCO always passes `withAgentAPIKeyExport = false` (community path)
  - If confirmed, remove the `withAgentAPIKeyExport` parameter and inline the `false` branch
  - Update MCO call sites

- [ ] Build + test:
  ```bash
  go build ./...
  go test ./mongodb-community-operator/controllers/construct/... -count=1 -v 2>&1 | tail -20
  ```

- [ ] Commit:
  ```bash
  git commit -m "refactor(decouple): Plan 10b — simplify MCO StatefulSet builder; remove AppDB-specific params"
  ```

---

### Task 9: Full build and test gate for PR 10b

- [ ] Full build: `go build ./...`
- [ ] Full unit tests for affected packages:
  ```bash
  go test ./controllers/operator/... ./mongodb-community-operator/controllers/... -count=1 2>&1 | grep -E "FAIL|ok"
  ```
- [ ] Confirm `MongoDBAssumeEnterpriseEnv` is gone from MCO:
  ```bash
  grep -rn "MongoDBAssumeEnterpriseEnv" --include="*.go" ./mongodb-community-operator/
  ```
  Expected: no output.
- [ ] Final lint check (`source venv/bin/activate && make precommit`) if time permits.
- [ ] Commit any lint fixes, push for PR.

---

## Notes for implementer

- **`util.AgentContainerUtilitiesName`** — used in the `mongodbAgentUtilitiesContainer` copy inside `appdb_statefulset.go`. Verify it exists in `pkg/util/constants.go` before using it; if not, add it.
- **`AppDBSpec.GetAgentLogLevel()` etc.** — `AppDBSpec` was already satisfying MCO's `MongoDBStatefulSetOwner` interface (that's how it was being passed). After renaming to `AppDBStatefulSetOwner` the compiler will immediately catch any missing methods.
- **deepcopy** — no generated deepcopy changes needed; no new CRD types are introduced.
- **PR 10b dependency** — PR 10b must be stacked on PR 10a. Do not start Task 7 until Task 6's build gate is green.
- **MCO tests** — `build_statefulset_test.go` uses `MongodbUserCommand` and `BaseAgentCommand()` directly. After PR 10b simplifies the MCO signature, update those tests.
