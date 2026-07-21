# VM → Kubernetes Migration Bug Hunt — Plan & Results

**Date:** 2026-07-17
**Branch:** `vm-migration-feature-branch` (commit `5dc66aefc`)
**Plugin:** S3-downloaded `kubectl-mongodb` (Build: `5dc66aefc6c44c5800df1b1ea41036378bf07429`)
**OM:** `http://ec2-52-29-62-219.eu-central-1.compute.amazonaws.com:30880`
**Project:** `migration` (`6a5a3a7cb8444f7b5da85100`)
**Org:** `6a59eeeeb8444f7b5d9217d7`
**Namespace:** `bughunt-nam`
**Kubeconfig:** `bug-hunt/artifacts/bughunt.kubeconfig` (context `vm-migration-bughunt`)
**Plugin:** `bug-hunt/artifacts/kubectl-mongodb`

---

## Deployment State

> Values below are the **Step 4.8 post-restart snapshot** (~16:31-16:39Z on 2026-07-17) unless marked **[later, 16:39Z]**, **[A7 read, 2026-07-18T09:11Z]** (read-only, no mutations), **[A8 post-pair-1, 2026-07-18]** (state after the approved A8 pair-1 live mutations), **[A8 post-pair-2, 2026-07-20]** (state after the approved A8 pair-2 live mutations), or **[A8 post-pair-3, 2026-07-20]** (state after the approved A8 pair-3 live mutations — the most recent state). The later/A7 values come from read-only `kubectl get` passes that performed no mutations; the A8 rows reflect state after live spec mutations. Do not treat the point-in-time rows (notably the 5-process OM membership and the `Failed` CR phase) as the current state.

| Property | Value |
|---|---|
| Topology | Replica set (`rs0`) |
| Members (Step 4.8 snapshot) | vm-mongodb-0 (PRIMARY), vm-mongodb-1 (SECONDARY), vm-mongodb-2 (SECONDARY), rs0-0 (SECONDARY) — point-in-time |
| Members **[later, 16:39Z]** | K8s pods rs0-0, rs0-1, rs0-2 all Running (rs0-1/rs0-2 now exist); vm-mongodb-0..4 all Running. RS membership was not re-queried via mongosh in the later pass. |
| Auth | SCRAM-SHA-256, `autoUser=mms-automation` |
| TLS | `requireTLS`, `CAFilePath=/mongodb-automation/tls/ca/ca-pem`, `clientCertificateMode=OPTIONAL` |
| Users | `mms-automation` (admin roles), `app-user` (readWriteAnyDatabase) |
| Sentinel data | **Absent** — `bughunt.sentinel` count=0 (queried post-recovery; does not survive). Loss point unproven; see Bounded Recovery / Bug #4. |
| Agents (Step 4.8 snapshot) | 5 registered, all at goal state (goalVersion=12) — **point-in-time; do not read as current**. OM was later re-queried after A8 pairs 1, 2, and 3; see the A8 post-pair-3 row. |
| VM pods | 5/5 Running (rolling-restarted during bounded recovery; startTime ~16:31:34-39Z) |
| Operator | 1/1 Running (image `quay.io/mongodb/staging/mongodb-kubernetes:5dc66aef`) |
| MongoDB CR (Step 4.8 snapshot) | `rs0` — phase `Failed` (pre-existing; `NetworkConnectivityVerification=True`) — point-in-time |
| MongoDB CR **[later, 16:39Z]** | `rs0` — phase **`Running`**, version `7.0.12`; `NetworkConnectivityVerification=True`. Why/when it transitioned is not evidenced by the read-only pass. |
| MongoDB CR **[A7 read, 2026-07-18T09:11Z]** | `rs0` — phase **`Running`**, version `7.0.12`, `resourceVersion=493314`, `generation=4`, `spec.members=3` (votes:0), `externalMembers=3`. Unchanged before/after A7 dry-run (spec-sha256 `3fc5c273…` identical). Read-only; no mutation. |
| MongoDB CR **[A8 post-pair-1, 2026-07-18]** | `rs0` — phase **`Running`**, `observedGeneration=6`, `spec.members=3` (rs0-0 votes=1 priority=1; rs0-1/rs0-2 votes=0), `externalMembers=2` (rs0_0, rs0_1; rs0_2 pruned). `rs.status`: 5 healthy in-sync (vm-mongodb-0 PRIMARY, vm-mongodb-1, rs0-0, rs0-1, rs0-2). `rs.conf`: 3 voters (VM0, VM1, rs0-0) + 2 K8s passives. All rs0 PVCs Bound, all pods Running 0 restarts, sentinel 0. **OM verified** (Digest-auth read-only GET `/automationStatus` → 200): `goalVersion=15`, exactly 5 processes, removed `rs0_2` absent, every remaining process `lastGoalVersionAchieved=15`, `errorCode=0`, empty `plan`. Stale `NetworkConnectivityVerification` observedGeneration=3 (observation, not a confirmed bug). |
| MongoDB CR **[A8 post-pair-2, 2026-07-20]** | `rs0` — phase **`Running`**, `observedGeneration=8`, `spec.members=3` (rs0-0 votes=1 priority=1; rs0-1 votes=1 priority=1; rs0-2 votes=0), `externalMembers=1` (rs0_0 only; rs0_1 pruned). `rs.status`: 4 healthy in-sync (vm-mongodb-0 PRIMARY, rs0-0, rs0-1, rs0-2). `rs.conf`: 3 voters (VM0, rs0-0, rs0-1) + 1 K8s passive (rs0-2). All rs0 PVCs Bound, all pods Running 0 restarts, sentinel 0. **OM verified** (Digest-auth read-only GET `/automationStatus` → 200): `goalVersion=17`, exactly 4 processes, `rs0_1` absent, every remaining process `lastGoalVersionAchieved=17`, `errorCode=0`, empty `plan`. Stale `NetworkConnectivityVerification` observedGeneration=3 (observation, not a confirmed bug). |
| MongoDB CR **[A8 post-pair-3, 2026-07-20]** | `rs0` — phase **`Running`**, `observedGeneration=10`, `spec.members=3` (rs0-0 votes=1 priority=1; rs0-1 votes=1 priority=1; rs0-2 votes=1 priority=1), `externalMembers=0` (empty — migration pruning complete). `rs.status`: 3 healthy in-sync (rs0-2 PRIMARY, rs0-0, rs0-1). `rs.conf`: 3 voters (rs0-0, rs0-1, rs0-2), 0 passives, 0 external. All rs0 PVCs Bound, all pods Running 0 restarts, sentinel 0. **OM verified** (Digest-auth read-only GET `/automationStatus` → 200): `goalVersion=20`, 3 processes, `rs0_0` absent, every remaining process `lastGoalVersionAchieved=20`, `errorCode=0`, empty `plan`. Election: rs0-2 elected primary at term 2 (13:22:56Z) after pruning vm-mongodb-0. Stale `NetworkConnectivityVerification` observedGeneration=3 (observation, not a confirmed bug). |
| TLS issuance | Recovered via namespace-local Issuer `bughunt-ca` (CA CN=bughunt-ca); shared `vm-bughunt-ca` ClusterIssuer remains broken |

---

## Attack Plan

### A1 — Raw generated CR admission contract

**Hypothesis:** The generator emits zero component counts (`mongodsPerShardCount=0`, `mongosCount=0`, `configServerCount=0`). The admission webhook (`mandatorySingleClusterFieldsAreSpecified`) rejects zero counts with no migration bypass. The E2E harness silently rewrites counts before applying.

**Steps:**
1. Run `kubectl mongodb migrate-to-mck mongodb` with the workshop ConfigMap/Secret.
2. Apply the raw generated YAML without editing.
3. Observe whether the webhook rejects it.

**Expected:** Rejection with `"The following fields must be specified in single cluster topology: mongodsPerShardCount, mongosCount, configServerCount"`.

**Status:** Completed
**Outcome:** Raw CR with `members: 0` (field omitted by `omitempty`) was **ACCEPTED** by the admission webhook. This contradicts the analyst's prediction that `replicasetMemberIsSpecified` would reject it. The operator immediately began dry-run connectivity validation. The validator either has a migration bypass for `Members==0` when `externalMembers` is present, or the webhook is not enforcing this validator for migration CRs. **Not a bug** — the operator handles `members=0` + `externalMembers` correctly (`desiredReplicas:0, isScaling:false`).

Also confirmed **A15** (version mismatch gate): locally-built plugin stamping `1.8.0` was rejected by webhook (`Operator is on version 5dc66aef`). S3 plugin stamps matching version.

---

### A1b — Retry with dedicated config server deployment

**Hypothesis:** Dry-run should validate connectivity to VM members without mutating the deployment.

**Steps:**
1. Rewrite component counts to non-zero (matching E2E harness minimums).
2. Apply with `mongodb.com/migration-dry-run=true`.
3. Watch for `NetworkConnectivityVerification` condition = PASS.

**Expected:** PASS.
**Status:** Completed
**Outcome:** PASS. Connectivity job `rs0-connectivity-check` completed in 4s. All 3 external members pinged individually and confirmed reachable. Job logs show: connected to MongoDB, ping succeeded, each member reachable via TLS (CA at `/mongodb-automation/tls/ca/ca-pem`).

Note: First attempt failed because `rs0-ca` ConfigMap and `mdb-rs0-cert` Secret didn't exist yet. After creating them (copying CA from `vm-mongodb-cert` Secret), re-applying the CR passed.

**Finding:** Deleting a migration CR with `migration-dry-run=true` wipes the OM automation config — all processes and replica sets removed from OM, agents shut down mongod. This is destructive: a dry-run CR deletion destroys the source VM deployment. The AC version jumped from 1 to 5 on deletion.

---

### A3 — Dry-run connectivity with wrong CA

**Hypothesis:** TLS is OPTIONAL, so wrong CA may not cause a failure — potentially masking a real TLS issue when TLS is later required.

**Steps:**
1. Apply the dry-run CR but reference a wrong CA ConfigMap.
2. Observe whether connectivity validation passes or fails.

**Expected:** If TLS is optional, may pass incorrectly. If it fails, capture the condition.
**Status:** Not started — window passed (migration complete, CR no longer in dry-run)
**Outcome:** Not testable post-migration. The CR is no longer in dry-run mode (`externalMembers` is empty, migration pruning complete). Re-triggering dry-run would require creating a new CR or reverting the migration — risky and low value.

---

### A4 — Dry-run with one VM member unreachable

**Hypothesis:** The connectivity job may not test all members individually, or may mask individual failures.

**Steps:**
1. Scale one VM pod to 0 temporarily.
2. Run dry-run connectivity.
3. Restore the pod.
4. Observe whether the job identifies the specific unreachable host.

**Expected:** FAIL with specific host identification.
**Status:** Not started — not testable (VM members no longer in RS)
**Outcome:** Not testable post-migration. VM members are no longer replica set members (all pruned in A8). The connectivity check only runs during dry-run, which is no longer active.

---

### A5 — Migrate: add K8s members with zero votes/priority

**Hypothesis:** Operator should add K8s members alongside external VM members without disrupting quorum.

**Steps:**
1. Remove dry-run annotation.
2. Raise component counts, set `memberConfig` with `votes: 0, priority: "0"`.
3. Watch status transitions and OM goal state.
4. Verify sentinel data (if any) survives.

**Expected:** K8s members added, VM members unchanged, status → Running.
**Status:** Completed (transition path reconstructed from operator logs, 2026-07-20)
**Outcome:** The live CR `rs0` end-state was verified (2026-07-18T09:11Z) and the full transition path was reconstructed from operator logs (2026-07-20). The CR went through at least two incarnations (the CR was deleted and recreated at least once) and two distinct Failed phases before reaching Running:

1. **Failed #1 (2026-07-17T14:37:41Z):** Dry-run connectivity check failed — "Network connectivity failed". Resolved by deleting and recreating the CR and creating the missing TLS resources (rs0-ca ConfigMap and mdb-rs0-cert Secret, Step 3.5).
2. **Failed #2 (2026-07-17T15:47:14Z):** rs0-0 agent couldn't reach READY state — "automation agents haven't reached READY state during defined interval". The agent was stuck for ~45 minutes before recovering at 16:32:59Z.
3. **Scaling (2026-07-17T16:32:59Z–16:33:55Z):** After rs0-0 agent recovered, the operator scaled incrementally: desiredReplicas 0→1→2→3. Each step was triggered by the previous pod's agent reaching READY state in OM. rs0-1 was created at 16:33:02Z and rs0-2 at 16:33:55Z (53 seconds apart).
4. **Running (2026-07-17T16:35:04Z):** All 3 agents reached READY state. CR transitioned to phase=Running, observedGeneration=4, members=3. memberConfig initially set to [{votes:0,priority:0},{votes:0,priority:0},{votes:0,priority:0}] — all K8s members non-voting. rs0-0 was promoted to votes:1/priority=1 later at generation 5 (A8 pair 1, Step 7.2 on 2026-07-18).

The Step 4.8 "Failed" snapshot is confirmed — the CR was indeed in Failed phase (Failed #2) at that time. Sentinel data was already confirmed absent (count=0) at Step 4.8; not re-queried here.

---

### A6 — Pod restart during initial sync

**Hypothesis:** Restarting a K8s pod during initial sync may cause duplicate process names or stuck sync.

**Steps:**
1. Delete one K8s pod during initial sync.
2. Observe recovery behavior.

**Expected:** Pod restarts and sync resumes.
**Status:** Not started — window passed (initial sync complete since Jul 17)
**Outcome:** Not testable post-migration. All 3 K8s members have been healthy and in-sync since 2026-07-17T16:35:04Z. Initial sync is complete; there is no ongoing sync to interrupt.

---

### A7 — Invalid voting configuration (>7 voters)

**Hypothesis:** The operator or admission should reject >7 voting members.

**Steps:**
1. Set member counts high enough that total voters exceed 7.
2. Apply and observe rejection.

**Expected:** Validation error.
**Status:** Completed (admission-only dry-run, 2026-07-18T09:11-09:13Z)
**Outcome:** **Admission does NOT reject >7 voting members.** Verified via `kubectl apply --dry-run=server` (server-side dry-run; nothing persisted, live CR not mutated — before/after fingerprint identical: `resourceVersion=493314`, `generation=4`, `spec-sha256=3fc5c273…`).

Four dry-run tests were run (full detail in execution-log.md Step 5):
- **TEST 0** (UPDATE `rs0`, combined add-members + votes, 8 voters): **DENIED** — `"only one migration change type is allowed per update: adding Kubernetes members, removing external members, or updating member votes/priority"`. This is a migration change-type validator in the running operator image (`5dc66aef`), NOT a >7-voter check. It is not present in the checked-out source tree.
- **TEST 1** (CREATE `rs0-a7test`, 8 voters > 7, no old object): **ACCEPTED** — `rs0-a7test created (server dry run)`. The admission validator set (`RunValidations`) contains no voter-count check.
- **TEST 2** (UPDATE, votes-only 0→1, 6 voters): **ACCEPTED** — single change type allowed; 6 voters (not >7).
- **TEST 3** (UPDATE, add members 3→8, votes unchanged at 0, 3 voters): **ACCEPTED** — single change type allowed; 3 voters (not >7).

**Key findings:**
1. The `mdbpolicy.mongodb.com` webhook has **no >7-voter validation**. A CR with 8 voting members passes admission (TEST 1).
2. The migration change-type validator blocks UPDATEs that combine "adding K8s members" with "updating votes/priority" (TEST 0). From the current live state (3 external voters + 3 non-voting K8s), >7 voters **cannot be reached in a single UPDATE** — it requires two change types. This is an indirect safety effect, not an explicit voter-count check.
3. Source inspection (PR800, `/Users/nam.nguyen/projects/mongodb-kubernetes-pr800`): for migration CRs (externalMembers present) the 7-voter limit is enforced at **reconcile time** by `validateACForMigration` (`controllers/operator/common_controller.go:459-494`, called from `mongodbreplicaset_controller.go:237`) → `validateVotingLimitRS` (`:590-594`) → `validateVotingLimit` (`:556-572`), which returns `workflow.Failed(...)` with a detailed error when total voters > `MaxVotingMembers` (7) — it **fails the reconcile, does NOT silently coerce**. `Deployment.limitVotingMembers` (`controllers/om/deployment.go:1310-1325`) explicitly **no-ops when externalMembers are present** (`if len(externalMembers) > 0 { return }`, `:1311-1313`); it only auto-zeroes excess voters for pure-K8s deployments (no external members). **Retraction:** the earlier "silently coerces" claim was read from the master tree's `limitVotingMembers` (`controllers/om/deployment.go:1177-1190`, signature without the `externalMembers` guard) and incorrectly applied to migration; it is false for migration and retracted.

**Reconcile-time behavior (source-proven per PR800, not runtime-observed):** No >7-voter CR was persisted, so runtime reconcile behavior was not observed. PR800 source establishes the failure path: `validateACForMigration` → `validateVotingLimitRS` → `validateVotingLimit` returns `workflow.Failed(...)` for migration CRs with >7 total voters, and `limitVotingMembers` no-ops when externalMembers are present. So a persisted >7-voter migration CR would fail reconciliation (expected `phase=Failed`), NOT be silently coerced. The "silently coerces" claim is retracted as false for migration. Runtime confirmation (persisting such a CR and observing `phase=Failed`) was not performed.

**Artifacts:** `bug-hunt/artifacts/a7-candidate*.yaml` (non-secret manifests).

---

### A8 — Prune: remove external members one at a time

**Hypothesis:** Pruning should remove VM members safely, maintaining quorum.

**Steps:**
1. Promote K8s members (votes/priority).
2. Remove external members one at a time via JSON patch.
3. After each removal verify: quorum, primary availability, sentinel data, OM goal state, CR status.

**Expected:** `externalMembers` → empty, status → `MigrationComplete` / `Running`.
**Status:** Complete — all 3 pairs executed (2026-07-20).
**Outcome:** **All 3 pairs succeeded; irreversible external-member removal confirmed for all three.** Full detail in execution-log.md Steps 7 (pair 1), 8 (pair 2), and 11 (pair 3).

**Plan:** 3 promote/prune pairs (promote one K8s member to `votes=1, priority=1`, then remove one external member). All 3 pairs executed.

**Preflight (gen 4, read-only):** CR `rs0` `Running`, `observedGeneration=4`; six healthy in-sync members (vm-mongodb-0 PRIMARY, vm-mongodb-1, vm-mongodb-2, rs0-0, rs0-1, rs0-2); 3 `gp3` PVCs Bound; sentinel count 0.

**Mutation 1 — promote rs0-0 (memberConfig[0]) to votes=1, priority=1 (gen 5):** `rs.conf()` confirmed **four voters** (vm-mongodb-0, vm-mongodb-1, vm-mongodb-2, rs0-0); all six members healthy. Single migration change type ("updating member votes/priority") accepted.

**Mutation 2 — remove externalMembers[2] rs0_2 / vm-mongodb-2 (gen 6):** Single migration change type ("removing external members") accepted. **Irreversible** — the pruned external member cannot be re-added without re-running the migration add-member path.

**Final read-only verification (gen 6, phase `Running`):**
- CR `rs0`: phase `Running`, `observedGeneration=6`; `spec.externalMembers` = **rs0_0, rs0_1** (2 remain).
- `rs.status()`: exactly **five healthy members** (vm-mongodb-0, vm-mongodb-1, rs0-0, rs0-1, rs0-2) with **identical optime**; primary **vm-mongodb-0**.
- `rs.conf()`: exactly **3 voters** (vm-mongodb-0, vm-mongodb-1, rs0-0) and **two K8s passives** (rs0-1, rs0-2, votes=0).
- All rs0 PVCs Bound; all pods Running, 0 restarts; sentinel count 0 (unchanged).

**OM public API verification — Verified (Digest-authenticated read-only GET):** A Digest-authenticated read-only GET of `/api/public/v1.0/groups/$PROJ_ID/automationStatus` with the existing `my-credentials` Secret credentials succeeded (HTTP 200). `automationStatus.goalVersion=15`; exactly **five processes** present; the removed `rs0_2` is **absent**; every remaining process had `lastGoalVersionAchieved=15`, `errorCode=0`, and an empty `plan` — all agents in goal state at the post-prune membership. No credential values recorded.

**Observation — stale `NetworkConnectivityVerification` condition (observedGeneration 3):** The condition's `observedGeneration` is 3 while the CR is at `observedGeneration=6` (pair 1) and later `observedGeneration=8` (pair 2). **Recorded as an observation, NOT a confirmed bug** — may be an expected artifact of the migration reconcile path not re-running connectivity validation once the CR is already `Running`/`NetworkConnectivityVerification=True`, or may indicate the condition is not refreshed on migration-only spec changes. Root cause not investigated.

**Pair 2 (executed 2026-07-20, full detail in execution-log.md Step 8):**

**Preflight (gen 6, read-only, 2026-07-20T12:40Z):** CR `rs0` `Running`, `observedGeneration=6`; 5 healthy in-sync members (vm-mongodb-0 PRIMARY, vm-mongodb-1, rs0-0, rs0-1, rs0-2); `rs.conf` 3 voters (VM0, VM1, rs0-0) + 2 K8s passives (rs0-1, rs0-2); `externalMembers=2` (rs0_0, rs0_1); OM `goalVersion=15`, 5 processes all at 15/errorCode=0/empty plan; all PVCs Bound, all pods Running 0 restarts, sentinel 0. Quorum arithmetic: 3 voters/majority 2 → after promote 4 voters/majority 3 → after prune 3 voters/majority 2; all states maintain live majority.

**Mutation 1 — promote rs0-1 (memberConfig[1]) to votes=1, priority=1 (gen 7):** Single migration change type ("updating member votes/priority") accepted. `rs.conf()` confirmed **4 voters** (vm-mongodb-0, vm-mongodb-1, rs0-0, rs0-1); 1 passive (rs0-2); all 5 members healthy, identical optimes. OM `goalVersion=16`, 5 processes all at 16/errorCode=0/empty plan. Post-promotion gate: ALL 7 criteria PASS.

**Mutation 2 — remove externalMembers[1] rs0_1 / vm-mongodb-1 (gen 8):** Single migration change type ("removing external members") accepted. **Irreversible** — the pruned external member cannot be re-added without re-running the migration add-member path.

**Final read-only verification (gen 8, phase `Running`, 2026-07-20T12:46Z):**
- CR `rs0`: phase `Running`, `observedGeneration=8`; `spec.externalMembers` = **rs0_0 only** (rs0_1 removed; 1 external remains).
- `rs.status()`: exactly **4 healthy members** (vm-mongodb-0, rs0-0, rs0-1, rs0-2) with **identical optime**; primary **vm-mongodb-0** (unchanged since Jul 17, term=1). vm-mongodb-1 is no longer a member.
- `rs.conf()`: exactly **3 voters** (vm-mongodb-0, rs0-0, rs0-1) and **one K8s passive** (rs0-2, votes=0). `configVersion=7`, `term=1`.
- All rs0 PVCs Bound; all pods Running, 0 restarts; sentinel count 0 (unchanged).

**OM public API verification — Verified (Digest-authenticated read-only GET):** A Digest-authenticated read-only GET of `/api/public/v1.0/groups/$PROJ_ID/automationStatus` with the existing `my-credentials` Secret credentials succeeded (HTTP 200). `automationStatus.goalVersion=17`; exactly **four processes** present; the removed `rs0_1` is **absent**; every remaining process had `lastGoalVersionAchieved=17`, `errorCode=0`, and an empty `plan` — all agents in goal state at the post-prune membership. No credential values recorded.

**Post-prune verification: ALL 8 criteria PASS.**

**Pair 3 (executed 2026-07-20, full detail in execution-log.md Step 11):**

**Preflight (gen 8, read-only, 2026-07-20T13:18Z):** CR `rs0` `Running`, `observedGeneration=8`; 4 healthy in-sync members (vm-mongodb-0 PRIMARY, rs0-0, rs0-1, rs0-2); `rs.conf` 3 voters (VM0, rs0-0, rs0-1) + 1 K8s passive (rs0-2); `externalMembers=1` (rs0_0 only); OM `goalVersion=17`, 4 processes all at 17/errorCode=0/empty plan; all PVCs Bound, all pods Running 0 restarts, sentinel 0. Nodes all DiskPressure=False (87% node recovered). Quorum arithmetic: 3 voters/majority 2 → after promote 4 voters/majority 3 → after prune 3 voters/majority 2; all states maintain live majority. Pruning the primary forces an election.

**Mutation 1 — promote rs0-2 (memberConfig[2]) to votes=1, priority=1 (gen 9):** Single migration change type ("updating member votes/priority") accepted. `rs.conf()` confirmed **4 voters** (vm-mongodb-0, rs0-0, rs0-1, rs0-2), 0 passives; all 4 members healthy, identical optimes. OM `goalVersion=18`, 4 processes all at 18/errorCode=0/empty plan. Primary still vm-mongodb-0 (no election triggered). Post-promotion gate: ALL 7 criteria PASS.

**Mutation 2 — remove externalMembers[0] rs0_0 / vm-mongodb-0 (the PRIMARY) (gen 10):** Single migration change type ("removing external members") accepted. **Irreversible** — the pruned external member cannot be re-added without re-running the migration add-member path. This forced a MongoDB election since vm-mongodb-0 was the current primary.

**Final read-only verification (gen 10, phase `Running`, 2026-07-20T13:24Z):**
- CR `rs0`: phase `Running`, `observedGeneration=10`; `spec.externalMembers` = **[]** (empty — migration pruning complete).
- `rs.status()`: exactly **3 healthy members** (rs0-0, rs0-1, rs0-2) with **identical optime**; vm-mongodb-0 is no longer a member.
- **NEW PRIMARY: rs0-2** (`_id=5`) — elected at 2026-07-20T13:22:56Z, term=2. Clean election: rs0-0 voted for rs0-2, all members in-sync, no stale primary, no split brain.
- `rs.conf()`: exactly **3 voters** (rs0-0, rs0-1, rs0-2), 0 passives, 0 external. `configVersion=10`, `term=2`.
- All rs0 PVCs Bound; all pods Running, 0 restarts; sentinel count 0 (unchanged).

**OM public API verification — Verified (Digest-authenticated read-only GET):** A Digest-authenticated read-only GET of `/api/public/v1.0/groups/$PROJ_ID/automationStatus` with the existing `my-credentials` Secret credentials succeeded (HTTP 200). `automationStatus.goalVersion=20`; exactly **three processes** present; the removed `rs0_0` is **absent**; every remaining process had `lastGoalVersionAchieved=20`, `errorCode=0`, and an empty `plan` — all agents in goal state at the post-prune membership. No credential values recorded.

**Post-prune verification: ALL 9 criteria PASS.**

**A8 attack complete:** All 3 pairs executed. `externalMembers` is now empty. The replica set is a pure 3-member Kubernetes-native set with all voters, no external members, no passives. Migration pruning is complete.

**Artifacts:** None written for A8 (live CR patches; no non-secret manifest artifacts produced).

---

### A9 — Prune: delete current primary first

**Hypothesis:** Removing the current primary should trigger an election, not data loss.

**Steps:**
1. Identify the current primary.
2. Remove that external member first (out of safe order).
3. Observe election behavior and data continuity.

**Expected:** Election occurs, data survives, no stale primary.
**Status:** Not run — A8 pair 3 provides supporting evidence, but not the same ordering
**Outcome:** A8 pair 3 pruned vm-mongodb-0 while it was primary, and a clean election followed: rs0-2 became primary at term 2 (2026-07-20T13:22:56Z), with all remaining members in-sync and no stale primary or split brain. However, A8 removed the primary **last**, after the other external members were already gone; A9 proposed removing it **first**, while other external members remained. The exact A9 ordering was therefore not tested. Data preservation also cannot be claimed from the sentinel: its count was already 0 before A8.

---

### A10 — Sharded-specific: component certificate mismatch

**Hypothesis:** The walkthrough says "issue one cert per component" but the generated CR references a single `certsSecretPrefix`. Per-component cert Secret naming may not match what the operator expects.

**Steps:**
1. Generate CR with TLS enabled.
2. Inspect the per-component Secret names the operator looks for vs what the walkthrough creates.
3. Mismatch one component's cert and observe the failure.

**Expected:** Operator fails to find the correct Secret for one component.
**Status:** Completed (source inspection + live reproduction, 2026-07-20)
**Outcome:**
- The naming algorithm is consistent: generator sets certsSecretPrefix, operator derives per-component names.
- The instruction originates in the `migrate-to-mck mongodb` CLI help (`cmd/kubectl-mongodb/migrate-to-mck/mongodb.go:61-71`), which prints `kubectl create secret tls <certsSecretPrefix>-<resourceName>-cert ...`. The TLS validation warning repeats the same single-Secret instruction at `cmd/kubectl-mongodb/migrate-to-mck/validation.go:284-288`.
- The sharded controller instead resolves a Secret for each component at `controllers/operator/mongodbshardedcluster_controller.go:1328-1332`, using `MemberCertificateSecretName` with `MongosRsName`, `ConfigRsName`, and `ShardName(i)`. Example: prefix `mdb`, resource `mycluster`, and one shard produce `mdb-mycluster-mongos-cert`, `mdb-mycluster-config-cert`, and `mdb-mycluster-0-cert`; the documented command creates only `mdb-mycluster-cert`, which none of those components uses.
- Failure mode: reconciliation fails clearly when a required per-component Secret is missing or invalid; it is not silent. `controllers/operator/certs/certificates.go:425` wraps errors from certificate verification with `"The secret object '%s' does not contain all the valid certificates needed"`; the wrapped cause includes a Kubernetes NotFound error when the Secret is missing.

**Live reproduction (2026-07-20, namespace `bughunt-nam1`):**
- Set up a 3-process sharded cluster on `bughunt-nam1` (csrs-0 on vm-mongodb-0:27019, shard0-0 on vm-mongodb-1:27018, mongos-0 on vm-mongodb-2:27017). All 3 processes reached `lastGoalVersionAchieved=2`, `errorCode=0` — cluster healthy.
- Ran `kubectl-mongodb migrate-to-mck mongodb --config-map-name my-project --secret-name my-credentials --namespace bughunt-nam1 --certs-secret-prefix mdb` → generated CR `mycluster` with `spec.security.certsSecretPrefix: mdb`, `spec.security.tls.ca: mycluster-ca`, `spec.type: ShardedCluster`.
- CLI warning instructed: "create a kubernetes.io/tls Secret named `<certsSecretPrefix>-<resourceName>-cert`" → `mdb-mycluster-cert`.
- Created the CA ConfigMap `mycluster-ca` (key `ca-pem`) and the TLS Secret `mdb-mycluster-cert` (keys `tls.crt`, `tls.key`) exactly as instructed.
- Removed the `migration-dry-run` annotation and applied the CR.
- **Operator immediately set `phase=Failed`** with message: `"The secret object 'mdb-mycluster-mongos-cert' does not contain all the valid certificates needed: Secret "mdb-mycluster-mongos-cert" not found, The secret object 'mdb-mycluster-config-cert' does not contain all the valid certificates needed: Secret "mdb-mycluster-config-cert" not found, The secret object 'mdb-mycluster-0-cert' does not contain all the valid certificates needed: Secret "mdb-mycluster-0-cert" not found"`.
- The operator looked for 3 per-component Secrets (`mdb-mycluster-mongos-cert`, `mdb-mycluster-config-cert`, `mdb-mycluster-0-cert`) — none existed. The single Secret `mdb-mycluster-cert` created per CLI guidance was never looked up.
- CR status: `phase=Failed`, `observedGeneration=1`, `lastTransition=2026-07-20T16:04:11Z`.
- Operator log line (verbatim): `"Updating status: phase=Failed, options=[{Message:The secret object 'mdb-mycluster-mongos-cert' does not contain all the valid certificates needed: Secret \"mdb-mycluster-mongos-cert\" not found, The secret object 'mdb-mycluster-config-cert' does not contain all the valid certificates needed: Secret \"mdb-mycluster-config-cert\" not found, The secret object 'mdb-mycluster-0-cert' does not contain all the valid certificates needed: Secret \"mdb-mycluster-0-cert\" not found} {Warnings:[]} {ResourcesNotReady:[]}]"`.

- A user following the walkthrough literally for a sharded cluster would create a Secret that no component looks up.
- Severity: Medium — documentation/instruction bug, not a code defect. Operator fails safely but user guidance is misleading for sharded migrations.
- Source: PR800 tree at /Users/nam.nguyen/projects/mongodb-kubernetes-pr800, commit 5dc66aefc.
- Artifacts: `/tmp/bughunt-nam1-sharded-cr.yaml` (generated CR), `/tmp/bughunt-nam1-sharded-ac.json` (AC pushed to OM).

---

### A11 — Sharded-specific: prune order (config → shard → mongos)

**Hypothesis:** Pruning in the wrong order (e.g. mongos before config server) may leave the cluster in an inconsistent state.

**Steps:**
1. Attempt to prune mongos external members before config server members.
2. Observe whether the operator prevents this or allows breakage.

**Expected:** Either rejected or causes inconsistency.
**Status:** Completed (live reproduction, 2026-07-21, namespace `bughunt-nam2`)
**Outcome:** **The operator does NOT validate or enforce prune order for sharded clusters. Wrong-order pruning is accepted, and the CR reports `Running` with no error or warning.**

**Live reproduction (2026-07-21, namespace `bughunt-nam2`):**
- Set up a 3-process sharded cluster on `bughunt-nam2` (csrs-0 on vm-mongodb-0:27019, shard0-0 on vm-mongodb-1:27018, mongos-0 on vm-mongodb-2:27017). All 3 processes reached `lastGoalVersionAchieved=1`, `errorCode=0` — cluster healthy.
- Generated and applied a `MongoDB` CR (`mycluster`, type=ShardedCluster) with correct per-component TLS Secrets (`mdb-mycluster-mongos-cert`, `mdb-mycluster-config-cert`, `mdb-mycluster-0-cert`). CR reached `phase=Running`.
- **Wrong-order prune:** Patched `spec.externalMembers` to remove `mongos-0` (the query router) while leaving `csrs-0` (config server) and `shard0-0` (shard) as external members.
- **Admission accepted the patch** — no validation error. The migration change-type validator (`mongodb_validation.go:645-655`) only checks that K8s members aren't removed and external members aren't added; it does NOT check the order of external member removal.
- **Operator removed mongos-0 from the OM automation config** (goal version jumped 2→3). The OM AC now has only 2 processes (csrs-0, shard0-0); mongos-0 is absent. The sharding config still references the full cluster (`myCluster` with `configServerReplica: csrs` and shard `shard0` → `shard0-rs`).
- **CR status remained `Running`** — `phase=Running`, `observedGeneration=2`, no error message. The operator did not detect or report the inconsistent state.
- **The mongos process is still running on vm-mongodb-2** (PID 79, `mongos -f .../mongos-mongos-0.conf`). The agent on vm-mongodb-2 is stuck at `lastGoalVersionAchieved=2` while the goal is 3 — it hasn't processed the new goal version yet. The mongos is still functional: `db.adminCommand({ping:1})` returns `ok:1`, `sh.status()` shows the full cluster intact (1 shard, 1 active mongos, balancer running).
- **StatefulSets created with 0 count**: `mycluster-mongos` (0/0), `mycluster-config` (0/0), `mycluster-0` (0/0). The operator is ready to create K8s pods for all components but hasn't scaled any up.

**Key finding:** A user following the wrong prune order (mongos first) would:
1. Remove mongos from `externalMembers` → operator removes it from OM AC.
2. The CR shows `Running` — no indication of a problem.
3. The mongos is still running on the VM but unmanaged by OM (agent stuck at old goal version).
4. If the user then removes the config server or shard, the cluster could break without warning.
5. The operator provides no guidance, validation, or warning for prune order.

**Source code confirmation (PR800):** No prune-order validation exists in the sharded cluster controller or admission webhook. `checkExternalMembersDrift` (`common_controller.go:429-443`) only checks that CR external members match AC processes — after the operator removes mongos-0 from the AC, the check passes. `validateACForMigration` (`common_controller.go:459-494`) validates TLS mode and voting members limit, not prune order. The migration change-type validator (`mongodb_validation.go:645-655`) only checks that K8s members aren't removed and external members aren't added.

**Severity:** Medium — the operator doesn't prevent wrong-order pruning, and the CR status doesn't reflect the inconsistent state. A user could break a sharded cluster by pruning in the wrong order without any error indication. The operator should either enforce prune order (reject wrong-order pruning) or warn the user about the consequences.

**Artifacts:** `/tmp/bughunt-nam2-sharded-cr.yaml` (generated CR), `/tmp/bughunt-nam2-sharded-ac.json` (AC pushed to OM).

---

### A12 — Sharded-specific: query through mongos after migration

**Hypothesis:** After migration, querying through a K8s mongos should show all shard data.

**Steps:**
1. Complete migration.
2. Exec into a K8s mongos pod and run `sh.status()` + sentinel query.
3. Verify chunk distribution and data continuity.

**Expected:** All shards visible, data intact.
**Status:** Not run manually — covered by existing PR #800 sharded e2e tests
**Outcome:** The manual bughunt deployment has no mongos. However, the existing sharded migration e2e suite inserts sentinel data through mongos and verifies it after migration and promotion (`docker/mongodb-kubernetes-tests/tests/vm_migration/vm_migration_common_helper.py:151-166`, used by the sharded tests). Running that existing suite is preferable to creating another equivalent cluster.

---

### A13 — `Process.Port()` nil dereference on missing `net` section

**Hypothesis:** `process.go:210-215` uses a non-comma-ok type assertion on `p.Args()["net"]`. If a process lacks a `net` key, the plugin panics.

**Steps:**
1. Inspect whether any process in the current AC lacks `net.port`.
2. If not, craft a minimal AC mutation (in a separate test) that removes `net` and re-run generation.

**Expected:** Panic with `interface conversion: interface is nil, not map[string]interface{}`.
**Status:** Completed (code inspection + safe unit repro, 2026-07-18)
**Outcome:** **Code defect VERIFIED; production triggerability assessed (Warning / Low-Medium).** No live cluster reproduction.

PR800 source inspection (`/Users/nam.nguyen/projects/mongodb-kubernetes-pr800`) confirms the unguarded path: `Process.Port()` (`controllers/om/process.go:210-215`) performs `p.Args()["net"].(map[string]interface{})["port"]` — a non-comma-ok type assertion. `p.Args()` (`:320-322`) → `util.ReadOrCreateMap(p, "args2_6")` (`pkg/util/util.go:122-127`) creates `args2_6` if absent but does NOT create the nested `net` sub-map, so an absent `net` key yields `nil` and `nil.(map[string]interface{})` panics. The `ok` in the `if` header is bound by the map index `["port"]`, not by the type assertion, so it does not guard the assertion. Call sites: `controllers/om/replicaset.go:389` (`ExtractExternalMembers` → `proc.Port()`, reached from the plugin at `cmd/kubectl-mongodb/migrate-to-mck/sharded_cluster_generator.go:181`) and `controllers/om/deployment.go:657` (`CheckProcessFields` → `process.Port()`, guarded against a nil process at `:653-655` but NOT against an absent `net` key; operator-built processes always carry `net.port`, so this site is not a demonstrated trigger).

**Missing test:** `controllers/om/process_test.go:335-353` — `TestPort_ReturnsEmptyWhenNotSet` uses `args2_6: {"net": map[string]interface{}{}}` (the `net` key IS present, empty), so it does NOT exercise the absent-`net`-key panic path. No test covers the panic.

**Safe repro (unit-level, no cluster/OM/CR):** Standalone Go program replicating `Port()` verbatim, called on a `Process` with `args2_6: {}` (no `net` key), run with `go 1.26.5` → `PANIC: interface conversion: interface {} is nil, not map[string]interface {}` (recovered, exit 0). Confirms the panic mechanism and exact message. **Not a live cluster reproduction** — exercises the isolated code shape only.

**Triggerability (assessed 2026-07-20):**
- No validation in the plugin checks for `net` key presence. validateProcessesAreValid (validation.go:422-451) only checks processType and disabled.
- The AC is parsed via json.Unmarshal with zero validation/normalization of process fields. EnsureNetConfig() exists but is only used on the operator's process-creation path, never on the AC-parsing path.
- Live AC (2026-07-20): all 4 processes have args2_6.net.port=27017. No violations.
- Every inspected repository fixture also contains `args2_6.net`; no fixture demonstrates a valid process without it.
- Whether OM's server-side schema guarantees `net` for every process is outside the source tree. The code does not rely on this guarantee.
- **Can it actually be missing?** A raw JSON automation config can omit the map and plugin validation would accept that shape, so the Go code is defensively unsafe. But no evidence found shows Ops Manager accepting or emitting such a process. Treat this as a latent crash path with unverified production reachability, not a demonstrated production scenario.

**Severity (conservative):** Warning / Low-Medium. If triggered, impact is a plugin crash during CR generation (DoS of the migration tool), recoverable by re-running after correcting the AC. No data loss or cluster corruption implied by the panic itself.

---

### A14 — `ExtractMemberInfo` panic on missing process in processMap

**Hypothesis:** `replicaset.go:400-406` indexes `processMap[members[0].Name()]` without checking existence. A nil `Process` → `nil.(string)` panic.

**Steps:**
1. Check whether all RS member names have matching entries in the process map.
2. If all match, note as a latent bug not triggered by current state.

**Expected:** Latent panic — not triggered by current AC.
**Status:** Completed (code inspection + safe unit repro, 2026-07-18)
**Outcome:** **Code defect VERIFIED; production triggerability assessed (Warning / Low-Medium).** No live cluster reproduction.

PR800 source inspection (`/Users/nam.nguyen/projects/mongodb-kubernetes-pr800`) confirms the unguarded path: `ExtractMemberInfo` (`controllers/om/replicaset.go:400-406`) does `firstProc := processMap[members[0].Name()]` (`:404`) then `firstProc.Version()` (`:405`). `Process` is `type Process map[string]interface{}` (`process.go:123`), so a missing key yields a nil `Process`; `Version()` (`process.go:325-327`) does `p["version"].(string)` → on a nil map `p["version"]` is nil → `nil.(string)` panics. `FeatureCompatibilityVersion()` (`:406`, `process.go:486-491`) is nil-guarded and would be safe, but `Version()` is called first and panics first. The per-member loop (`:409-419`) has the same unguarded shape (`proc := processMap[host]` at `:411`, then `proc.Args()`/`proc.HostName()` on a nil map). Call sites are plugin-only: `cmd/kubectl-mongodb/migrate-to-mck/replica_set_generator.go:23` and `cmd/kubectl-mongodb/migrate-to-mck/sharded_cluster_generator.go:51`, both consuming `rs.Members()` and `ac.Deployment.ProcessMap()` from the OM automation config. No operator-side (reconcile) call site was found.

**Missing test:** `rg 'ExtractMemberInfo' --type go -g '*_test.go'` across the PR800 tree returns no matches — there is no test for `ExtractMemberInfo` at all (neither happy path nor the missing-process panic path).

**Safe repro (unit-level, no cluster/OM/CR):** Standalone Go program replicating the head of `ExtractMemberInfo` (`processMap[memberName]` → `Version()`), called with a `processMap` missing the member name, run with `go 1.26.5` → `PANIC: interface conversion: interface {} is nil, not string` (recovered, exit 0). Confirms the panic mechanism and exact message. **Not a live cluster reproduction** — exercises the isolated code shape only.

**Triggerability (assessed 2026-07-20):**
- No validation checks that ALL RS members have process entries. pickSourceProcess (validation.go:85-96) uses an ok check but only errors if NO voting+priority member is found — if at least one is present, validation passes even when other members are orphaned.
- A partial guard exists in extractInternalClusterAuthMode (common_spec.go:158-170) with a proper ok-check, but it runs AFTER ExtractMemberInfo (the panic site), so it does not protect.
- Live AC (2026-07-20): all 4 RS member hosts match process names exactly. Zero orphaned members.
- Every inspected repository fixture also preserves the replica-set-member → process-name relationship.
- Whether OM can produce an orphaned RS member is outside the source tree. The code does not rely on this guarantee.
- **Can this actually happen?** A malformed or inconsistent raw automation config can contain a dangling member name, and plugin validation does not reject it. But no evidence found shows Ops Manager accepting or emitting that state. Treat this as a latent crash path with unverified production reachability, not a demonstrated production scenario.

**Severity (conservative):** Warning / Low-Medium. If triggered, impact is a plugin crash during CR generation (DoS of the migration tool), recoverable by re-running after correcting the AC. No data loss or cluster corruption implied by the panic itself.

---

### A15 — Plugin/operator version mismatch gate

**Hypothesis:** `generatedResourceUsedCorrectImportToolVersion` rejects a CR whose `mongodb.com/migrate-tool-version` annotation doesn't match the running operator's version.

**Steps:**
1. Generate CR with the downloaded plugin (version from S3).
2. Check what version annotation it sets.
3. Compare with the operator's version (check operator logs or deployment image).
4. If they differ, apply and observe rejection.

**Expected:** Rejection if versions differ.
**Status:** Completed (confirmed at A1, 2026-07-17)
**Outcome:** Confirmed. A locally-built plugin stamping `1.8.0` was rejected by the `mdbpolicy.mongodb.com` admission webhook: `"The resource was generated with import tool version 1.8.0. Operator is on version 5dc66aef."` The S3-downloaded plugin stamps the matching version `5dc66aef` and was accepted. See execution-log.md Step 3.2.

---

### A16 — User migration with no auth enabled

**Hypothesis:** Auth is currently disabled. The `users` subcommand may still generate a MongoDBUser CR, but reconciliation may fail or produce unexpected behavior since there's no auth mechanism configured.

**Steps:**
1. Run `kubectl mongodb migrate-to-mck users` against the current AC.
2. Observe whether it generates a CR or errors.
3. If a CR is generated, apply it and observe reconciliation.

**Expected:** Either generation error or reconciliation failure.
**Status:** Premise false — auth IS enabled
**Outcome:** Not applicable. The hypothesis states "Auth is currently disabled" but the live deployment has SCRAM-SHA-256 enabled (`autoUser=mms-automation`, `deploymentAuthMechanisms=["SCRAM-SHA-256"]`). The premise is false; the attack does not apply to this deployment.

---

## Summary Tracker

| ID | Attack | Status | Result |
|---|---|---|---|
| A1 | Raw CR admission | Completed | Accepted — `members:0` not rejected (contradicts prediction) |
| A1b | Embedded config server | N/A | Correctly blocked by plugin validation |
| A15 | Version mismatch gate | Completed | Confirmed — `1.8.0` rejected, `5dc66aef` accepted |
| A2 | Dry-run correct CA | Completed | PASS — all 3 external members reachable, job completed in 4s |
| A3 | Dry-run wrong CA | Not started — window passed | Migration complete; CR no longer in dry-run. Re-triggering would require a new CR or reverting the migration — risky, low value. |
| A4 | Dry-run unreachable member | Not started — not testable | VM members no longer in RS (all pruned in A8). Connectivity check only runs during dry-run, which is no longer active. |
| A5 | Migrate with zero votes | Completed (transition path reconstructed from operator logs, 2026-07-20) | CR went through at least 2 incarnations (deleted and recreated at least once) and 2 Failed phases (connectivity failure, agent readiness timeout) before reaching Running at 16:35:04Z. Scaling was incremental 0→1→2→3, each triggered by agent READY. All memberConfig votes:0 at gen 4; rs0-0 promoted at gen 5 (A8 pair 1). |
| A6 | Pod restart during sync | Not started — window passed | Initial sync complete since 2026-07-17T16:35:04Z. All 3 K8s members healthy and in-sync; no ongoing sync to interrupt. |
| A7 | >7 voters invalid | Completed (admission dry-run) | Admission ACCEPTS >7 voters (CREATE dry-run, 8 voters). No voter-count validator in webhook. Migration change-type validator blocks combined UPDATEs (indirect). Reconcile-time (PR800 source-proven, not runtime-observed): `validateACForMigration`→`validateVotingLimitRS`→`validateVotingLimit` FAILS reconcile for migration >7 voters; `limitVotingMembers` no-ops when externalMembers present. Earlier "silently coerces" claim retracted as false for migration. |
| A8 | Prune one at a time | Complete (all 3 pairs, 2026-07-20) | **Pair 1** (2026-07-18): promoted rs0-0 (votes=1, priority=1, gen5 → 4 voters, 6 healthy) then removed externalMembers[2] rs0_2/vm-mongodb-2 (gen6). Final (gen6, Running): 5 healthy in-sync, primary vm-mongodb-0, rs.conf 3 voters (VM0, VM1, rs0-0) + 2 K8s passives, externalMembers=rs0_0/rs0_1, all PVCs Bound, all pods Running 0 restarts, sentinel 0. OM verified: goalVersion=15, 5 processes, rs0_2 absent, all errorCode=0/empty plan. **Pair 2** (2026-07-20): promoted rs0-1 (votes=1, priority=1, gen7 → 4 voters, 5 healthy) then removed externalMembers[1] rs0_1/vm-mongodb-1 (gen8). Final (gen8, Running): 4 healthy in-sync, primary vm-mongodb-0, rs.conf 3 voters (VM0, rs0-0, rs0-1) + 1 K8s passive (rs0-2), externalMembers=rs0_0 only, all PVCs Bound, all pods Running 0 restarts, sentinel 0. OM verified: goalVersion=17, 4 processes, rs0_1 absent, all errorCode=0/empty plan. **Pair 3** (2026-07-20): promoted rs0-2 (votes=1, priority=1, gen9 → 4 voters, 4 healthy) then removed externalMembers[0] rs0_0/vm-mongodb-0 the PRIMARY (gen10). Election triggered: rs0-2 elected new primary at term 2 (13:22:56Z). Final (gen10, Running): 3 healthy in-sync, primary rs0-2, rs.conf 3 voters (rs0-0, rs0-1, rs0-2), externalMembers=[] (empty), all PVCs Bound, all pods Running 0 restarts, sentinel 0. OM verified: goalVersion=20, 3 processes, rs0_0 absent, all errorCode=0/empty plan. Irreversible removal confirmed for all 3 pairs. A8 ATTACK COMPLETE: externalMembers empty, pure 3-member K8s-native set, all voters, no passives. Observation: stale NetworkConnectivityVerification observedGeneration=3 (not a confirmed bug). |
| A9 | Prune current primary first | Not run; partial evidence from A8 | A8 pruned the primary last and observed a clean election. It did not test the proposed prune-first ordering, and sentinel count was already 0, so data preservation was not proven. |
| A10 | Component cert mismatch | Completed (source inspection + live reproduction, 2026-07-20) | CLI help (`mongodb.go:61-71`) and validation (`validation.go:284-288`) instruct one `{prefix}-{resourceName}-cert`; the sharded controller (`mongodbshardedcluster_controller.go:1328-1332`) resolves `-config-cert`, `-mongos-cert`, and `-{i}-cert`. **Live-reproduced on `bughunt-nam1`**: applied CR with only `mdb-mycluster-cert` (as CLI guides) → `phase=Failed`, operator looked for `mdb-mycluster-mongos-cert`, `mdb-mycluster-config-cert`, `mdb-mycluster-0-cert` — all 3 not found. |
| A11 | Wrong prune order | Completed (live reproduction, 2026-07-21) | **Live-reproduced on `bughunt-nam2`**: removed mongos-0 from externalMembers while csrs-0 and shard0-0 remain external. Admission accepted, operator removed mongos-0 from OM AC (goalVersion 2→3), CR stayed `Running` with no error. Mongos still running on VM (agent stuck at v2), still functional (ping ok:1, sh.status() intact). No prune-order validation exists in source. Severity: Medium — operator doesn't prevent or warn about wrong-order pruning. |
| A12 | Query through mongos | Covered upstream; not run manually | Existing PR #800 sharded migration e2e tests insert and verify migration data through mongos. |
| A13 | Port() nil dereference | Completed (code inspection + safe unit repro) | Code defect VERIFIED (PR800 `controllers/om/process.go:210-215` non-comma-ok type assertion on absent `net` key → panic `interface conversion: interface {} is nil, not map[string]interface {}`, reproduced in isolation with `go 1.26.5`). Missing test: `process_test.go:346-353` only covers `net` present (empty), not absent. Production triggerability UNVERIFIED — OM AC schema guarantees unknown; no live cluster reproduction. Severity: Warning / Low-Medium triggerability. Live AC assessed 2026-07-20: all 4 processes have net.port=27017, no violations. OM schema guarantees unknown. |
| A14 | ExtractMemberInfo panic | Completed (code inspection + safe unit repro) | Code defect VERIFIED (PR800 `controllers/om/replicaset.go:400-406` unguarded `processMap[members[0].Name()]` → nil `Process` → `Version()` (`process.go:325-327`) `nil.(string)` panic `interface conversion: interface {} is nil, not string`, reproduced in isolation with `go 1.26.5`). No test exists for `ExtractMemberInfo` (rg returns no matches in `*_test.go`). Plugin-only call sites: `replica_set_generator.go:23`, `sharded_cluster_generator.go:51`. Production triggerability UNVERIFIED — OM AC schema guarantees unknown; no live cluster reproduction. Severity: Warning / Low-Medium triggerability. Live AC assessed 2026-07-20: all 4 RS member hosts match process names, zero orphaned. OM schema guarantees unknown. |
| A15 | Version mismatch gate | Completed | Confirmed — `1.8.0` rejected, `5dc66aef` accepted |
| A16 | User migration no auth | Premise false | Auth IS enabled (SCRAM-SHA-256, autoUser=mms-automation). The hypothesis "auth is currently disabled" is wrong. |
| R1 | Bounded recovery (local CA Issuer) | Completed | bughunt-nam cert issuance recovered; RS/OM healthy at Step 4.8 snapshot (point-in-time, not current); sentinel lost (count=0, loss point unproven — Step 4.7 rolling restart is a plausible direct cause); shared issuer still broken. Later read-only obs (16:39Z): CR `rs0` Running, rs0-1/rs0-2 exist. |

---

## Bounded Recovery — Namespace-Local CA Issuer (bughunt-nam only)

**Trigger:** Accidental shared issuer corruption left `bughunt-nam/vm-mongodb-cert` un-issuable (Secret deleted, shared `vm-bughunt-ca` ClusterIssuer signing broken).

**Root cause:** The shared `vm-bughunt-ca` Secret in `cert-manager` ns became internally inconsistent — `ca.crt` key holds the original CA (CN=vm-bughunt-ca, sha1 `3A:CF…`) while `tls.crt`/`tls.key` were overwritten with the `/tmp/bughunt-certs` pair (CN=bughunt-ca, sha1 `60:7E…`). The self-signed Certificate's key-spec tracking broke (`Ready=False`, `SecretMismatch`). The ClusterIssuer reports `Ready=True` but signing fails ("certificate chain is malformed or broken").

**Approach:** Recover only `bughunt-nam` using a namespace-local cert-manager CA Issuer backed by the existing CA pair at `/tmp/bughunt-certs/{ca.crt,ca.key}` (CN=bughunt-ca). No resource outside `bughunt-nam` mutated. No private-key bytes written to repo or logs.

**Mutations (all in bughunt-nam):**
- `secret/bughunt-ca-key-pair` — created (CA key-pair from /tmp files).
- `issuer.cert-manager.io/bughunt-ca` — created (namespace-local CA Issuer); `Ready=True`, `KeyPairVerified`.
- `certificate.cert-manager.io/vm-mongodb-cert` — patched `issuerRef` `ClusterIssuer/vm-bughunt-ca` → `Issuer/bughunt-ca`.
- `certificaterequest/vm-mongodb-cert-7` — deleted (stale failed).
- `secret/vm-mongodb-cert` — recreated by cert-manager (4 keys).
- `sts/vm-mongodb` (5 pods) — rolling restart.
- `bug-hunt/artifacts/bughunt-ca-issuer.yaml` — written (non-secret manifest).

**Certificate verification (pre-restart, all PASS):**
- Leaf issuer = CN=bughunt-ca; subject CN=vm-mongodb.bughunt-nam.svc.cluster.local.
- All 6 VM DNS SANs present (vm-mongodb + vm-mongodb-0..4).
- Issued `ca.crt` sha1 `60:7E…` == `/tmp/bughunt-certs/ca.crt` → CA key match.
- `openssl verify` OK; leaf cert/key modulus match.

**Post-restart verification (Step 4.8 snapshot, ~16:31-16:39Z — point-in-time, not current):**
- Mounted certs in vm-mongodb-0: CA=CN=bughunt-ca (sha1 `60:7E…`), leaf issuer=CN=bughunt-ca, 6 SANs.
- vm-mongodb-0 agent: "All 1 Mongo processes are in goal state", "In Goal State for clusterConfig version: 11", TLS auth succeeded.
- rs0-0 (K8s pod): 1/1 Running, TLS auth succeeded.
- RS status: vm-mongodb-0=PRIMARY, vm-mongodb-1/2=SECONDARY, rs0-0=SECONDARY, all health=1. (Only rs0-0 was a K8s RS member at this snapshot.)
- MongoDB CR `rs0`: phase `Failed` (pre-existing, unchanged); `NetworkConnectivityVerification=True`. (Stale by the later observation below — CR is `Running` at 16:39Z.)
- OM membership: goalVersion=12, all 5 processes `lastGoalVersionAchieved=12`, `errorCode=0`, empty plans. **This exact 5-process membership is a snapshot; do not read as current** (OM not re-queried in the later pass).

**Later read-only observation (2026-07-17T16:39:18Z, no mutations):** MongoDB CR `rs0` phase=`Running`, version `7.0.12`, `NetworkConnectivityVerification=True`; StatefulSet `rs0` 3/3; K8s pods rs0-0/rs0-1/rs0-2 all Running (rs0-1, rs0-2 now exist — absent at the Step 4.8 snapshot); vm-mongodb-0..4 all Running (startTime ~16:31:34-39Z, coincident with the Step 4.7 rolling restart). Why/when the CR reached `Running` is not evidenced by this read-only pass; OM membership was not re-queried.

**Sentinel:** Queried via mongosh over TLS as `mms-automation` (SCRAM-SHA-256). `bughunt.sentinel` count = **0** — does **not** survive. The exact moment of loss was not directly observed and is **unproven**. Because the VM pods mount `mongodb-data` as non-persistent `emptyDir`, any pod recreation wipes that volume. Two plausible direct causes: (a) the earlier A1b events (dry-run CR deletion wiped the OM automation config, shut down mongod, followed by pod restarts); and (b) the Step 4.7 rolling restart of `sts/vm-mongodb` performed during this bounded recovery, which recreated all 5 emptyDir-backed vm-mongodb pods (~16:31Z) and wiped their data volumes. (b) is a plausible direct cause occurring within this recovery itself; the loss cannot be attributed solely to earlier events, and the relative contribution of (a) vs (b) is not established.

**Cross-namespace immutability:** Before/after diff of all ClusterIssuers, Certificates, Secrets (excluding bughunt-nam), and Issuers — only two changes, both in `bughunt-nam` (vm-mongodb-cert rv bump + new bughunt-ca Issuer). Zero resources outside `bughunt-nam` changed. cert-manager ns `vm-bughunt-ca` Secret rv unchanged (477178).

**Shared issuer status:** Remains broken. cert-manager ns Certificate `vm-bughunt-ca` still `Ready=False` (`SecretMismatch`). Broken for future issuance/renewal; other namespaces' existing cached Certificates remain `Ready=True`.

---

## Bugs Found

| # | Severity | Attack | Description | Repro | Status |
|---|---|---|---|---|---|
| 1 | Info | A15 | Version mismatch gate works correctly — plugin version must match operator version | Apply CR with version `1.8.0` against operator `5dc66aef` | Confirmed |
| 2 | Info | A1 | `members:0` CR accepted by admission — `replicasetMemberIsSpecified` does not block migration CRs with externalMembers | Apply raw generated CR with no `members` field | Confirmed |
| 3 | High | R1 | Accidental shared issuer corruption: `cert-manager/vm-bughunt-ca` Secret has mismatched `ca.crt` (CN=vm-bughunt-ca) vs `tls.crt`/`tls.key` (CN=bughunt-ca) — self-signed Certificate `Ready=False`, ClusterIssuer signing fails ("certificate chain is malformed or broken"). Remains broken for future issuance. | Inspect `cert-manager` ns Certificate `vm-bughunt-ca` conditions + Secret key fingerprints | Confirmed (not fixed; out of scope — bounded to bughunt-nam) |
| 4 | Low | R1 | Sentinel data does not survive — `bughunt.sentinel` count=0 post-recovery (absence queried, not assumed). VM pods use non-persistent `emptyDir` for `mongodb-data`, so any pod recreation wipes the volume. Loss point is **unproven**: plausible direct causes are (a) earlier A1b AC-wipe + pod restarts and (b) the Step 4.7 rolling restart of `sts/vm-mongodb` during this bounded recovery, which recreated all 5 emptyDir-backed pods (~16:31Z). (b) occurs within the recovery itself, so causality is not deflected to earlier events alone. Downgraded Medium→Low: data loss is an expected consequence of non-persistent emptyDir under pod recreation (environmental, not a confirmed product defect) and the cause is unproven. | Query `bughunt.sentinel` after recovery | Confirmed (absence queried); cause unproven |
| 5 | Low | A7 | Admission webhook has **no >7-voter validation**: a MongoDB ReplicaSet CR with 8 voting members (3 external + 5 K8s, all votes=1) passes `mdbpolicy.mongodb.com` admission (verified via CREATE `--dry-run=server`). **Scope/impact:** for migration CRs with external members, reconcile has a safety check — `validateACForMigration`→`validateVotingLimitRS`→`validateVotingLimit` (`controllers/operator/common_controller.go:459/590/556`) — that fails reconciliation when total voters exceed 7. `Deployment.limitVotingMembers` (`controllers/om/deployment.go:1310-1325`) no-ops when external members are present; pure-K8s deployments retain their different behavior of auto-zeroing excess votes. **Recommendation:** add a migration-specific admission validator that rejects `external voting members + configured K8s voting members > 7`, sharing the counting logic with reconcile. Test CREATE and UPDATE at 7 and 8 voters, including omitted `memberConfig` entries (which default non-voting). A plugin warning when the source AC already has 7 voters would improve UX but cannot protect later CR edits. | `kubectl apply --dry-run=server -f a7-candidate-create.yaml` (8 voters) | Confirmed admission gap; migration reconcile safety source-proven, not runtime-observed |
| 6 | Info | A7 | Migration change-type validator in running operator image (`5dc66aef`) rejects UPDATEs combining multiple migration change types: `"only one migration change type is allowed per update: adding Kubernetes members, removing external members, or updating member votes/priority"`. Not present in checked-out source tree. Indirectly prevents reaching >7 voters in a single UPDATE from the current state (requires both add-members and votes changes). | `kubectl apply --dry-run=server -f a7-candidate.yaml` (combined UPDATE) | Confirmed (UPDATE denied); validator source not in tree |
| 7 | Info (hypothesis) | A7 | **Secondary observation — unverified reconcile-time validation-gap hypothesis, NOT a confirmed bug.** TEST 3 used `spec.members: 8` with only **3** `spec.memberConfig` entries (all `votes: 0`); admission accepted the length mismatch (single change-type UPDATE, 3 voters). Voting-limit path source-inspected in PR800 (`/Users/nam.nguyen/projects/mongodb-kubernetes-pr800`): `computePostReconcileVoting` (ReplicaSet, `controllers/operator/common_controller.go:690-719`) and `votingPositionsFromConfig` (sharded, `:575-587`) default missing `memberConfig` entries to `MemberOptions{}` (votes:0 → non-voting), so for TEST 3's exact spec reconcile would compute 0 K8s voting + 3 external = 3 ≤ 7 → `validateVotingLimit` passes — **this spec is NOT a >7-voter vector**. The broader question (whether reconcile handles a `members`/`memberConfig` length mismatch safely in all respects — StatefulSet replicas, per-pod memberConfig application, AC merge) is NOT proven by inspecting only the voting-limit path, and no CR with this mismatch was persisted (TEST 3 was a server-side dry-run). Labeled hypothesis pending either full reconcile-path source inspection or persisting such a CR and observing reconcile. | `kubectl apply --dry-run=server -f a7-candidate-add-members-only.yaml` (members=8, 3 memberConfig) | Unverified hypothesis (admission accepts; reconcile gap not source-proven beyond voting limit) |
| 8 | Warning (Low-Medium triggerability, unverified) | A13 | **Code defect VERIFIED; production triggerability UNVERIFIED.** `Process.Port()` (`controllers/om/process.go:210-215`, PR800) uses a non-comma-ok type assertion `p.Args()["net"].(map[string]interface{})["port"]`. `p.Args()` → `util.ReadOrCreateMap(p, "args2_6")` (`pkg/util/util.go:122-127`) creates `args2_6` if absent but NOT the nested `net` sub-map, so an absent `net` key yields `nil` → `nil.(map[string]interface{})` panics. The `ok` in the `if` header is bound by the map index `["port"]`, not the type assertion, so it does not guard it. Safe unit repro (standalone Go, `go 1.26.5`, no cluster/OM/CR) reproduces `PANIC: interface conversion: interface {} is nil, not map[string]interface {}`. Call sites: `controllers/om/replicaset.go:389` (`ExtractExternalMembers` ← plugin `sharded_cluster_generator.go:181`) and `controllers/om/deployment.go:657` (`CheckProcessFields`, nil-process-guarded at `:653-655` but not absent-`net`-guarded; operator-built processes always carry `net.port`). **Missing test:** `controllers/om/process_test.go:346-353` `TestPort_ReturnsEmptyWhenNotSet` uses `args2_6: {"net": {}}` (net present, empty) — does NOT cover the absent-`net` panic path. **Triggerability unverified:** requires an OM AC process lacking `net`; OM schema guarantees unknown. No live cluster reproduction. If triggered: plugin crash during CR generation (DoS of migration tool), recoverable by re-running after AC fix; no data loss/cluster corruption. | Standalone Go repro: `Process{args2_6: {}}` → `Port()` panics (recovered, exit 0) | Confirmed (code defect + isolated repro); production triggerability unverified |
| 9 | Warning (Low-Medium triggerability, unverified) | A14 | **Code defect VERIFIED; production triggerability UNVERIFIED.** `ExtractMemberInfo` (`controllers/om/replicaset.go:400-406`, PR800) does `firstProc := processMap[members[0].Name()]` (`:404`) without existence check, then `firstProc.Version()` (`:405`). `Process` is `map[string]interface{}` (`process.go:123`); a missing key yields a nil `Process`; `Version()` (`process.go:325-327`) does `p["version"].(string)` → on nil map `p["version"]` is nil → `nil.(string)` panics. `FeatureCompatibilityVersion()` (`:406`) is nil-guarded (`process.go:486-491`) but `Version()` is called first and panics first. The per-member loop (`:409-419`) has the same unguarded shape. Safe unit repro (standalone Go, `go 1.26.5`, no cluster/OM/CR) reproduces `PANIC: interface conversion: interface {} is nil, not string`. Call sites plugin-only: `cmd/kubectl-mongodb/migrate-to-mck/replica_set_generator.go:23`, `sharded_cluster_generator.go:51`; no operator-side call site found. **Missing test:** `rg 'ExtractMemberInfo' --type go -g '*_test.go'` returns no matches — no test exists for `ExtractMemberInfo` at all. **Triggerability unverified:** requires an OM AC with a RS member name orphaned from `processMap`; OM schema guarantees unknown. No live cluster reproduction. If triggered: plugin crash during CR generation (DoS of migration tool), recoverable by re-running after AC fix; no data loss/cluster corruption. | Standalone Go repro: `processMap` missing member name → `ExtractMemberInfo` head panics (recovered, exit 0) | Confirmed (code defect + isolated repro); production triggerability unverified |
| 10 | Info (observation — not a confirmed bug) | A8 | **Stale `NetworkConnectivityVerification` condition `observedGeneration`.** After A8 pair 1 (CR at `observedGeneration=6`), the `NetworkConnectivityVerification` condition still carries `observedGeneration=3` — it did not advance through generations 4, 5, 6 despite spec mutations (promote rs0-0, prune rs0_2) and the CR remaining `Running` with `NetworkConnectivityVerification=True`. **Recorded as an observation, NOT a confirmed bug:** may be expected behavior (the migration reconcile path may not re-run connectivity validation once the CR is already `Running` and the condition is already `True`), or may indicate the condition is not refreshed on migration-only spec changes. Root cause not investigated. | Inspect `rs0` status.conditions for `NetworkConnectivityVerification` `observedGeneration` vs CR `observedGeneration` after a migration spec change | Observation (not investigated) |

---

## Executive Summary

**What we did:** We tested the VM-to-Kubernetes migration plugin (PR #800, commit `5dc66aefc`) by running a real replica set migration — from 3 VM-based MongoDB members to 3 Kubernetes-native pods. The migration went through every phase: dry-run connectivity validation, adding K8s members with zero votes, incremental scaling, promoting K8s members to voters, and pruning all 3 external VM members one at a time (including the primary, which forced a clean election). The replica set ended as a healthy 3-member Kubernetes-native set with no external members.

**What we found (confirmed issues):**

1. **Sharded certificate naming mismatch (A10, Medium) — documentation/user-guidance bug, not an operator logic bug:** The operator correctly requires a separate certificate Secret for each sharded component. The defect is that the `migrate-to-mck mongodb` CLI help (`cmd/kubectl-mongodb/migrate-to-mck/mongodb.go:61-71`) and TLS warning (`validation.go:284-288`) give replica-set-only instructions even when generating a sharded CR.

   **Example input:** generate a two-shard resource named `myCluster` with `certsSecretPrefix: mdb`, then follow the printed command and create only `mdb-myCluster-cert`.

   **Expected by the operator:** `mdb-myCluster-config-cert`, `mdb-myCluster-mongos-cert`, `mdb-myCluster-0-cert`, and `mdb-myCluster-1-cert` (`mongodbshardedcluster_controller.go:1328-1332`).

   **Output:** reconciliation fails because those component Secrets are missing. `certificates.go:425` wraps the Kubernetes NotFound cause with a message saying the named Secret does not contain the required certificates; the wrapped cause reports that the Secret was not found. This output shape is source-traced, not live-reproduced in this manual bughunt. The fix is to make the CLI help/warning topology-aware; the operator naming algorithm does not need changing.

2. **Two latent panic paths in the migration plugin (A13, A14, Warning):** Two code paths can crash (Go panic) if the Ops Manager automation config is malformed or incomplete:
   - **A13:** `Process.Port()` (`process.go:210-215`) crashes if a process lacks a `net` key.
   - **A14:** `ExtractMemberInfo` (`replicaset.go:400-406`) crashes if a replica set member name has no matching process entry.
   
   Both were verified by source inspection and isolated unit-level reproduction. Plugin validation does not reject either malformed shape. However, every live process and inspected fixture had `net.port`, and every replica-set member matched a process; no evidence found shows Ops Manager accepting or emitting either malformed state. Production reachability is therefore unverified. If triggered, the impact is a recoverable plugin crash during CR generation, not data loss or cluster corruption.

3. **Shared CA issuer corruption (Bug #3, High — out of scope):** The shared cert-manager CA issuer (`vm-bughunt-ca`) was accidentally corrupted during testing — its Secret has mismatched `ca.crt` vs `tls.crt`/`tls.key`, breaking certificate signing. The bughunt namespace was isolated with a local issuer. The shared issuer remains broken for future issuance; other namespaces' existing cached certificates are unaffected. This is an environmental issue from the test setup, not a product defect.

4. **No >7-voter admission check (Bug #5, Low):** Kubernetes accepts a migration CR that the operator already knows cannot reconcile successfully.

   **Example input:** a ReplicaSet migration CR with 3 external voting members plus `members: 5` and five `memberConfig` entries with `votes: 1` — 8 voters total, above MongoDB's limit of 7.

   **Observed admission output:** `rs0-a7test created (server dry run)`. The webhook accepts the input instead of rejecting it.

   **Expected later reconcile output (source-proven, not runtime-observed):** `validateVotingLimit` returns `workflow.Failed`, so the CR is expected to transition to `phase=Failed`. The message reports that the post-reconcile replica set would have 8 voting members, exceeding the limit of 7, and tells the user to revert excess K8s members to `votes: 0` or prune external voters first.

   **Why this is a problem:** the invalid configuration is rejected late, after it has been stored, rather than immediately with an admission error. Recommended fix: add a migration-specific admission validator using the same voter-counting logic as reconcile. Test CREATE and UPDATE at 7 and 8 voters, including omitted `memberConfig` entries (which default non-voting). Do not apply this blanket behavior to pure-K8s CRs; those currently auto-zero excess votes.

**What we confirmed works:**
- **Dry-run connectivity validation** correctly pings each external VM member individually over TLS and reports reachability.
- **Zero-vote member addition** works — K8s members are added alongside external VM members without disrupting quorum.
- **Incremental scaling** (0→1→2→3) is gated by agent readiness in Ops Manager, one pod at a time.
- **One-at-a-time pruning** works — each promote/prune pair maintains quorum, and the migration change-type validator correctly enforces one change type per update.
- **Primary election on prune** works — pruning the current primary (vm-mongodb-0) triggered a clean election (rs0-2, term 2) with all members in-sync, no stale primary, no split brain.
- **Version mismatch gate** works — a plugin stamping the wrong version is rejected by the admission webhook.
- **`members:0` with `externalMembers`** is handled correctly by the operator (not rejected by admission, `desiredReplicas:0`).

**What should be tested manually:** Do not build another generic sharded migration—the existing suite already covers the normal path. Two focused negative tests remain useful:

1. **A10 — follow the bad certificate instruction literally.** In a disposable sharded migration namespace, generate a TLS-enabled CR with prefix `mdb`, create the CA ConfigMap and only the printed `mdb-<resource>-cert` Secret, then apply the CR. Verify:
   - Input: only the single documented Secret exists; component Secrets do not.
   - Output: reconciliation fails and names the missing `-config-cert`, `-mongos-cert`, or `-{i}-cert` Secret.
   - Recovery: create all expected component Secrets; reconciliation proceeds without changing the CR.

2. **A11 — wrong prune order.** First decide the intended contract: should mongos-first pruning be rejected, or is it supported? Then, in a disposable sharded migration, attempt to remove mongos external members **before** config-server and shard external members. Before and after the change, verify CR phase/message, Ops Manager goal state, config-server/shard/mongos membership, `sh.status()`, and a sentinel query through mongos. If the intended contract is rejection, the expected output is an admission/reconcile error with no topology change; if supported, the expected output is continued query availability and preserved sentinel data.

Use a disposable kind/isolated namespace, persistent volumes for sentinel data, and a namespace-local issuer—never the shared issuer used by other tests. Capture a complete baseline before either mutation.

For coverage already present upstream, run existing tests instead of repeating them manually:
- `e2e_vm_migration_shardedcluster_scram_sha256_tls` for correct per-component TLS setup and wrong-CA dry-run (A3).
- `e2e_vm_migration_shardedcluster_no_auth` for A16.
- Existing sharded migration tests already insert and verify data through mongos (A12).

**What this manual bughunt did not test (and why):**
- **A3 (wrong CA dry-run):** Window passed — the migration is complete and the CR is no longer in dry-run mode.
- **A4 (unreachable VM member):** Not testable — VM members are no longer in the replica set; the connectivity check only runs during dry-run.
- **A6 (pod restart during initial sync):** Window passed — initial sync completed on Jul 17; all members are healthy and in-sync.
- **A9 (prune primary first):** Not run. A8 pruned the primary last and observed a clean election, which is supporting evidence but not the same ordering; sentinel count was already 0, so this run cannot prove data preservation.
- **A11/A12 (sharded prune order / mongos query):** Not run manually because this deployment is a replica set. A12 is covered by existing upstream sharded e2e; A11 is not.
- **A16 (no-auth migration):** Not applicable to this manual deployment because auth is enabled; existing upstream no-auth sharded e2e coverage exists.

**Final cluster state (2026-07-20T13:24Z):**
- 3 healthy K8s members: rs0-0, rs0-1, rs0-2 — all voters (priority=1).
- Primary: rs0-2 (elected at term 2, 2026-07-20T13:22:56Z).
- `externalMembers`: empty (migration pruning complete).
- OM goalVersion=20, all 3 processes at goal state (errorCode=0, empty plan).
- All PVCs Bound, all pods Running with 0 restarts.
- Sentinel data: count=0 (does not survive — VM pods use non-persistent emptyDir).
- Stale `NetworkConnectivityVerification` observedGeneration=3 (observation, not a confirmed bug — not investigated).
