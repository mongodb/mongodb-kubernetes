# AppDB online-mode AC push → read-modify-update (Option C) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Replace the blind-PUT of a freshly-built headless automation config to the external (Meta) OM with the idiomatic read-modify-update pattern, so OM keeps its own `mongoDbVersions` (with real build URLs) and the operator only sets the AppDB's processes / replicaSet / auth / TLS.

**Architecture:** In `deployAutomationConfig` (online branch), build `om.Process` entries from the AppDB spec via `om.NewMongodProcess` (AppDBSpec satisfies `mdbv1.DbSpec`), assemble an `om.ReplicaSetWithProcesses`, configure SCRAM auth on the external connection via `authentication.Configure`, then `externalConn.ReadUpdateDeployment(closure)` that calls `MergeReplicaSet` + `ConfigureTLS` (+ monitoring/backup). None of these touch `mongoDbVersions`, so OM's manifest is preserved — fixing the `mongoDbVersions.builds.url` 400 at the root instead of stripping it.

**Tech Stack:** Go, controller-runtime, `controllers/om` (Deployment/Process/AutomationConfig), `controllers/operator/authentication`.

**Supersedes:** the interim fix (commit wiring up `stripUnsupportedACFields` before the blind PUT). Once this lands, remove that strip call and the `stripUnsupportedACFields` helper.

**Prereq context:** the interim strip fix must already be merged and the PIT DR e2e (`e2e_om_appdb_meta_om_mode_switch`) green, so this refactor is validated against a known-good baseline.

---

## Key references (verified during investigation)

- Online push today: `controllers/operator/appdbreplicaset_controller.go` `deployAutomationConfig` — `externalConn != nil` branch (~line 1954), currently `externalConn.UpdateAutomationConfig(omAC, log)` (blind PUT).
- `reconcileOMConnection` (~line 2330) opens `externalConn` via `connection.PrepareOpsManagerConnection`; **configures no auth and no deployment**.
- Canonical RS pattern: `controllers/operator/mongodbreplicaset_controller.go:740-771` — `replicaset.BuildFromMongoDBWithReplicas(...)` → `updateOmAuthentication(...)` → `conn.ReadUpdateDeployment(func(d){ ReconcileReplicaSetAC(...) })`. Note `BuildFromMongoDBWithReplicas` takes `*mdbv1.MongoDB` and **cannot** be reused for AppDB.
- `om.NewMongodProcess(name, hostName, mongoDBImage string, forceEnterprise bool, additionalConfig *mdbv1.AdditionalMongodConfig, spec mdbv1.DbSpec, certificateFilePath string, annotations map[string]string, fcv string) om.Process` — `controllers/om/process.go:122`.
- `om.NewReplicaSet(name, version)` (`replicaset.go:56`); `om.NewReplicaSetWithProcesses(rs, processes, memberOptions)` (`fullreplicaset.go:19`); `om.NewMultiClusterReplicaSetWithProcesses(rs, processes, memberOptions, existingProcessIds, connectivity)` (multi-cluster, preserves `_id`s).
- `Deployment.MergeReplicaSet(operatorRs, specArgs26, prevArgs26 map[string]interface{}, log)` (`deployment.go:146`); `Deployment.ConfigureTLS(security, caFilePath)` (`:107`); `Deployment.ConfigureMonitoringAndBackup(log, tls, caFilepath)` (`:260`). **None write `mongoDbVersions`** (confirmed; `Debug()` at `:639` even excludes it).
- Auth pattern (already used for monitoring at `appdbreplicaset_controller.go:1798-1815`): `authentication.Configure(ctx, r.client, conn, authentication.Options{AgentMechanism: util.SCRAM, Mechanisms: []string{util.SCRAM}, ClientCertificates: util.OptionalClientCertficates, AutoUser: util.AutomationAgentUserName, AutoPEMKeyFilePath: agentCertPath, CAFilePath: util.CAFilePathInContainer, MongoDBResource: types.NamespacedName{...}}, false, log)`.
- `om.WaitForReadyState(conn, processNames, suppressErrors bool, log)` — `controllers/om/automation_status.go:44`.
- Process names/hostnames: `generateProcessList(opsManager) []automationconfig.Process` (`:1392`, multi-cluster aware) and `generateProcessHostnames(opsManager) []string` (`:1371`).
- Member options/ids: `generateMemberOptions(...)` and `getExistingAutomationReplicaSetMembers(...)` (used in `buildAppDbAutomationConfig`); for OM, preserve `_id`s from OM's current deployment via the multi-cluster builder.
- Cert hash for TLS process arg: `enterprisepem.ReadHashFromSecret(ctx, r.SecretClient, ns, tlsSecretName, appdbSecretPath, log)` (computed today inside `buildAppDbAutomationConfig:1186`).
- `MockedOmConnection` for unit tests: `controllers/om/mockedomclient.go` (`om.NewMockedOmConnection(om.NewDeployment())`, `om.TestURL`, `om.TestGroupID`).

---

## Task 1: Extract a helper to build the AppDB `om.ReplicaSetWithProcesses`

**Files:**
- Modify: `controllers/operator/appdbreplicaset_controller.go`
- Test: `controllers/operator/appdbreplicaset_controller_test.go`

- [ ] **Step 1: Write the failing test** — `TestBuildAppDBOMReplicaSet`: construct a minimal `MongoDBOpsManager` (3-member AppDB, TLS off), call the new helper, assert it returns 3 processes with the expected names/hostnames (matching `generateProcessList`), the RS has 3 members, and the mongod version equals `rs.GetMongoDBVersion()`.

```go
func TestBuildAppDBOMReplicaSet(t *testing.T) {
	opsManager := DefaultOpsManagerBuilder().SetAppDBMembers(3).Build() // use existing test builder
	r := newAppDBReplicaSetReconcilerForTest(opsManager)               // existing helper used elsewhere in this file
	rsw, processNames, err := r.buildAppDBOMReplicaSet(opsManager, "" /*tlsCertPath*/)
	require.NoError(t, err)
	assert.Len(t, rsw.Processes, 3)
	assert.Len(t, processNames, 3)
	assert.Equal(t, r.generateProcessHostnames(opsManager)[0], rsw.Processes[0].HostName())
}
```

- [ ] **Step 2: Run it, confirm it fails** (`buildAppDBOMReplicaSet` undefined).

Run: `go test -mod=mod ./controllers/operator/ -run '^TestBuildAppDBOMReplicaSet$' -count=1`

- [ ] **Step 3: Implement `buildAppDBOMReplicaSet`** on `*ReconcileAppDbReplicaSet`:

```go
// buildAppDBOMReplicaSet builds the OM-side replica set (processes + members) for the AppDB,
// for use with ReadUpdateDeployment against an external OM. Unlike the headless community AC,
// these processes carry no mongoDbVersions — OM supplies the version manifest itself.
func (r *ReconcileAppDbReplicaSet) buildAppDBOMReplicaSet(opsManager *omv1.MongoDBOpsManager, tlsCertPath string) (om.ReplicaSetWithProcesses, []string, error) {
	rs := opsManager.Spec.AppDB
	procList := r.generateProcessList(opsManager)         // []automationconfig.Process (name+hostname, multi-cluster aware)
	memberOptions := r.generateMemberOptions(opsManager, nil)
	fcv := opsManager.CalculateFeatureCompatibilityVersion()

	processes := make([]om.Process, len(procList))
	for i, p := range procList {
		processes[i] = om.NewMongodProcess(
			p.Name, p.HostName,
			r.imageUrls[mcoConstruct.MongodbImageEnv],
			construct.IsEnterprise(),
			rs.GetAdditionalMongodConfig(),
			&rs,            // AppDBSpec satisfies mdbv1.DbSpec
			tlsCertPath,
			nil,
			fcv,
		)
	}

	omRs := om.NewReplicaSet(rs.Name(), rs.GetMongoDBVersion())
	rsWithProcesses := om.NewReplicaSetWithProcesses(omRs, processes, memberOptions)
	return rsWithProcesses, rsWithProcesses.GetProcessNames(), nil
}
```

- [ ] **Step 4: Run the test, confirm PASS.**
- [ ] **Step 5: Commit** — `git commit -m "feat(appdb): build OM-side replica set for online AC push"`

**Open item:** confirm `om.NewMongodProcess` sets the correct port / replSetName / storage path for AppDB the same way the headless builder does (`buildAppDbAutomationConfig:1236-1239`). If not, set them on the returned `om.Process` (`SetPort`, `SetReplicaSetName`, storage) to match.

---

## Task 2: Preserve replica-set member `_id`s across reconciles (multi-cluster-safe)

**Files:**
- Modify: `controllers/operator/appdbreplicaset_controller.go`
- Test: `controllers/operator/appdbreplicaset_controller_test.go`

- [ ] **Step 1: Failing test** — seed a `MockedOmConnection` whose deployment already has the RS with members at specific `_id`s; call the push; assert the re-pushed members keep those `_id`s (no churn). This guards against re-electing/reconfiguring on every reconcile.

- [ ] **Step 2: Run, confirm fail.**

- [ ] **Step 3: Implement** — switch Task 1's builder to `om.NewMultiClusterReplicaSetWithProcesses(omRs, processes, memberOptions, existingProcessIds, nil)` where `existingProcessIds` comes from the OM deployment fetched inside the `ReadUpdateDeployment` closure (`d.GetReplicaSetProcessIdsMap()` or equivalent — verify the accessor name in `deployment.go`). This requires building the RS *inside* the closure (after the GET) so existing ids are available.

- [ ] **Step 4: Run, confirm PASS. Step 5: Commit.**

---

## Task 3: Configure SCRAM auth on the external OM connection

**Files:**
- Modify: `controllers/operator/appdbreplicaset_controller.go`
- Test: `controllers/operator/appdbreplicaset_controller_test.go`

- [ ] **Step 1: Failing test** — with a `MockedOmConnection`, run the online push and assert the resulting deployment/AC has SCRAM enabled and the `mms-automation` auto user configured (the blind-PUT path got this from the community AC's `Auth`; RMU must add it explicitly).

- [ ] **Step 2: Run, confirm fail.**

- [ ] **Step 3: Implement** — before the `ReadUpdateDeployment` call, configure auth on `externalConn` mirroring `tryConfigureMonitoringInOpsManager:1798`:

```go
authOpts := authentication.Options{
	AgentMechanism:     util.SCRAM,
	Mechanisms:         []string{util.SCRAM},
	ClientCertificates: util.OptionalClientCertficates,
	AutoUser:           util.AutomationAgentUserName,
	AutoPEMKeyFilePath: agentCertPath, // thread through from deploy options
	CAFilePath:         util.CAFilePathInContainer,
	MongoDBResource:    types.NamespacedName{Namespace: opsManager.Namespace, Name: opsManager.Name},
}
if err := authentication.Configure(ctx, r.client, externalConn, authOpts, false, log); err != nil {
	return 0, workflow.Failed(xerrors.Errorf("failed to configure auth on external OM: %w", err))
}
```

- [ ] **Step 4: Run, confirm PASS. Step 5: Commit.**

**Open item:** `agentCertPath` is currently available in `tryConfigureMonitoringInOpsManager` and the reconcile flow but not in `deployAutomationConfig`'s signature — thread it through (it flows from `appdbOpts`/deployment options). Confirm whether AppDB online mode uses client certs at all; if not, the keyfile path (`util.AutomationAgentKeyFilePathInContainer`) may be the relevant input instead.

---

## Task 4: Replace the blind PUT with `ReadUpdateDeployment` and wait for goal state

**Files:**
- Modify: `controllers/operator/appdbreplicaset_controller.go` (online branch of `deployAutomationConfig`, ~line 1954)
- Test: `controllers/operator/appdbreplicaset_controller_test.go`

- [ ] **Step 1: Failing test** — `TestOnlinePushPreservesMongoDbVersions`: seed `MockedOmConnection` whose deployment already has `mongoDbVersions` with real build URLs; run the online push; assert (a) `ReadUpdateDeployment` was invoked, (b) the deployment's `mongoDbVersions` is **unchanged** (URLs intact), (c) processes/replicaSets now reflect the AppDB. This is the direct regression for the original 400.

- [ ] **Step 2: Run, confirm fail.**

- [ ] **Step 3: Implement** — replace the `externalConn != nil` block:

```go
if externalConn != nil {
	tlsCertPath := r.appdbTLSCertPath(ctx, opsManager, log) // extracted from buildAppDbAutomationConfig:1181-1186/1246
	if status := r.configureExternalOMAuth(ctx, opsManager, externalConn, agentCertPath, log); !status.IsOK() {
		return 0, status
	}
	var processNames []string
	err := externalConn.ReadUpdateDeployment(func(d om.Deployment) error {
		rsWithProcesses, names, err := r.buildAppDBOMReplicaSetWithExistingIds(opsManager, tlsCertPath, d)
		if err != nil {
			return err
		}
		processNames = names
		d.MergeReplicaSet(rsWithProcesses, rs.GetAdditionalMongodConfig().ToMap(), nil /*prevArgs26; see open item*/, log)
		d.ConfigureTLS(rs.GetSecurity(), util.CAFilePathInContainer)
		d.ConfigureMonitoringAndBackup(log, rs.GetSecurity().IsTLSEnabled(), util.CAFilePathInContainer)
		return nil
	}, log)
	if err != nil {
		return 0, workflow.Failed(xerrors.Errorf("failed to push AC to external OM: %w", err))
	}
	if err := om.WaitForReadyState(externalConn, processNames, false, log); err != nil {
		return 0, workflow.Failed(xerrors.Errorf("external OM agents did not reach goal state: %w", err))
	}
	return 0, workflow.OK()
}
```

- [ ] **Step 4: Run, confirm PASS. Step 5: Commit.**

**Open items:**
- `prevArgs26`: passing `nil` is safe on first push; for steady-state, thread the last-applied `AdditionalMongodConfig` (analogous to `mongodbreplicaset_controller.go:751` `lastRsConfig`) so obsolete `args2_6` keys are removed. Persist it in AppDB status/annotations if not already.
- Return value: the online path previously returned `config.Version`; with RMU there is no local AC version. Confirm callers (`deployAutomationConfigOnHealthyClusters`) tolerate `0` / adjust to OM's reported version.

---

## Task 5: Remove the interim strip code

**Files:**
- Modify: `controllers/operator/appdbreplicaset_controller.go`
- Test: `controllers/operator/appdbreplicaset_controller_test.go`

- [ ] **Step 1:** Delete the `stripUnsupportedACFields` call (no longer reached — the blind-PUT branch is gone) and the `stripUnsupportedACFields` function itself.
- [ ] **Step 2:** Delete `TestStripUnsupportedACFields` (its concern is now covered by `TestOnlinePushPreservesMongoDbVersions`).
- [ ] **Step 3:** `go build -mod=mod ./controllers/operator/` and `grep -rn stripUnsupportedACFields controllers/` → no matches.
- [ ] **Step 4: Commit** — `git commit -m "refactor(appdb): drop interim AC field-stripping after RMU rewrite"`

---

## Validation

- Unit: `go test -mod=mod ./controllers/operator/ -run 'AppDB|OnlinePush|StripUnsupported' -count=1`
- e2e: re-run `e2e_om80_kind_ubi` / `e2e_om_appdb_meta_om_mode_switch` (debug spawn host for fast iteration). The mode switch must pass and the suite must reach (and pass) the PIT disaster-recovery test.

## Risks / open questions (resolve during implementation)

1. **`om.NewMongodProcess` parity** with the headless builder for port / replSetName / storage / TLS arg / systemLog. Diff the resulting process map against the headless AC process to confirm equivalence.
2. **Auth inputs** (`agentCertPath` vs keyfile) for AppDB online mode — confirm what Meta OM expects.
3. **Member `_id` source** accessor name on `om.Deployment` for existing process ids (verify in `deployment.go`).
4. **Multi-cluster**: `generateProcessList` already spans member clusters; confirm the OM merge handles the full set and that scale-up/down still works.
5. **Monitoring interaction**: ensure this push (automation) and the existing monitoring registration against Primary OM don't conflict in online mode.
