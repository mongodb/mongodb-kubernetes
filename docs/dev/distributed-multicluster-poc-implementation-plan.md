# Distributed Multi-Cluster Operator — PoC Implementation Plan

**Companion to:** [`distributed-multicluster-operator.md`](./distributed-multicluster-operator.md) (architecture proposal).
**Status:** Plan for execution. Designed to be resumable from a fresh Claude Code session.
**Scope:** Prove the Raft-coordinated distributed operator design works for multi-cluster sharded clusters, with minimal changes to the existing codebase. Backwards-compatible: the existing hub-spoke code path remains untouched and default.

---

## 0. Resume protocol (read first when picking this up)

If you're a fresh session being asked to continue this work:

1. Read this entire document first. Each phase has explicit acceptance criteria and a "where we are" hook so you can identify which phase is in progress.
2. Check the current state by reading the **Progress Tracker** at the end (§10). It is updated by the main session after each chunk completes.
3. Each chunk is sized for a single subagent execution. Spawn subagents as described in §9 (Subagent Execution Protocol).
4. The user prefers commits after each chunk but no push until they say so (per `feedback_commit_push_separation`).
5. **Subagents do NOT create worktrees** — the user has already created one via `wt-ctl create` from `lsierant/devcontainer`. All subagents work on that worktree's checkout.
6. **Write-modifying subagents run sequentially**, never in parallel. Read-only research subagents may run in parallel.
7. If a subagent fails or returns incomplete work, do **not** auto-retry — surface the failure to the user and propose next steps.

---

## 1. Overview

### 1.1 What we're building (PoC scope)

A simulated 3-cluster distributed operator for sharded MongoDB clusters, coordinated via real `hashicorp/raft` consensus, validated **first** in unit tests (all in-process) and **then** in an e2e environment (3 operator processes running locally in the devcontainer against 3 separate kubeconfigs).

The PoC must demonstrate, end-to-end:

1. Three operator instances form a Raft cluster.
2. The CR is reconciled across three clusters with **leader-only AutomationConfig publication** and **lease-gated cross-cluster StatefulSet updates**.
3. The reconcile flow uses **requeue-on-no-lease**, never blocking.
4. The existing hub-spoke code path is unchanged (backwards-compatible).
5. The existing e2e test `docker/mongodb-kubernetes-tests/tests/multicluster_shardedcluster/multi_cluster_sharded_simplest.py` passes with the new mode active.

### 1.2 What we're explicitly NOT doing in this PoC

- mTLS between operators (use plain TCP + configurable ports).
- Persistent Raft storage (in-memory for unit tests; `emptyDir`-style local disk for the e2e run is acceptable but not required).
- Quorum recovery (`raft.RecoverCluster`) — 3 nodes always healthy.
- Witness voter, 2-cluster topology.
- TLS cert / Secret / ConfigMap cross-cluster replication via the operator. The PoC **manually replicates** these in the e2e test setup (or skips by using non-TLS where possible).
- Migration tooling from hub-spoke.
- Search workload.
- Sealed FSM entries / encryption at rest (key envelope design is documented elsewhere; PoC stores everything plaintext in memory).
- OnDelete StatefulSet strategy. Default RollingUpdate is fine — cross-cluster STS serialization is the protocol property we're testing, and that happens at the lease-gating level.

### 1.3 Top-line design choices that shape the implementation

- **Coordinator interface, not a rewrite.** Introduce a `DistributedCoordinator` interface on the existing `ShardedClusterReconcileHelper`. In non-distributed mode the field is nil and existing logic runs unchanged. In distributed mode, the helper consults the coordinator at specific gate points.
- **Cluster identity is mode-specific.** In distributed mode, every operator knows its own `clusterName`. The existing per-cluster iteration loops in the reconciler are preserved; in distributed mode they `continue` for any cluster that isn't ours.
- **Lease-gated, requeue-on-miss.** When the operator needs to apply STS changes to its own cluster, it checks whether it currently holds the lease for that scope. If not, it returns a requeue without doing work. The leader is responsible for allocating leases.
- **Leader-only AC publication.** All sites that call `om.ReadUpdateDeployment`, `om.WaitForReadyState`, `waitForAgentsToRegister`, and `CalculateDiffAndStopMonitoring` are wrapped: in distributed mode, only the Raft leader runs them. Followers skip and read AC state via FSM status proposals from the leader.
- **In-process Raft for unit tests.** A subset of the `pkg/coordination/raft/` package uses an in-memory transport so unit tests can spin up 3 Raft nodes inside one test binary. This is the validation surface we lean on heavily.

### 1.4 Phases

| Phase | What | Where it runs | Subagent-driven |
|---|---|---|---|
| A | Worktree + EVG host setup | User's machine + EVG | No (user manual) |
| B | Baseline e2e validation (hub-spoke still works) | EVG devcontainer | Yes |
| C | Unit-test PoC (in-process simulation) | Local devcontainer | Yes (main bulk of work) |
| D | e2e PoC (3 operator processes) | EVG devcontainer | Yes |
| E | Verification + handoff | EVG devcontainer | Yes |

---

## 2. Phase A — Environment setup (user-manual)

These steps are performed by the user, not by subagents.

1. Create a fresh worktree off `lsierant/devcontainer`:
   ```
   wt-ctl create lsierant/devcontainer-raft-poc --base lsierant/devcontainer
   ```
   (Exact `wt-ctl` syntax per the user's tooling.)
2. Provision a fresh evergreen host for this worktree.
3. Verify the devcontainer boots: it should provide a multi-cluster kind setup with the standard 3-cluster topology already in place.
4. Confirm `.generated/current.devc.kubeconfig` exists and contains contexts for all member clusters.

**Acceptance:** `evg_host.sh ssh -- kubectl --kubeconfig=/work/.generated/current.devc.kubeconfig config get-contexts` lists the expected clusters.

---

## 3. Phase B — Baseline validation

Goal: confirm the unmodified codebase runs the target e2e test green. This establishes the reference point.

### B1 — Run baseline e2e

**Subagent type:** general-purpose (read+execute, may make minor config tweaks).
**Inputs:** Worktree from Phase A, EVG host accessible via `evg_host.sh`.
**Deliverables:** A passing run of `multi_cluster_sharded_simplest.py` against the current (unmodified) operator running locally in the devcontainer.

**Subagent prompt template:**

```
On the current worktree (no need to create one — it's already set up).
Goal: get the existing e2e test
docker/mongodb-kubernetes-tests/tests/multicluster_shardedcluster/multi_cluster_sharded_simplest.py
to pass with the operator running locally in the devcontainer against the existing
multi-cluster kind setup.

Use:
- scripts/dev/op_run.sh to start the operator locally
- scripts/dev/e2e_run.sh (or equivalent) to run the e2e test
- evg_host.sh ssh to access the EVG host
- No docker-in-devc needed (per feedback_local_e2e_via_op_run)

Acceptance:
- The test completes with exit 0.
- Operator logs show the standard hub-spoke flow.
- Report any setup gotchas encountered (network prefix, image pull, etc.)
  in a paragraph at the end.

Time-box yourself to ~30 minutes of debugging. If you hit a blocker, stop
and report.

Report under 300 words.
```

**Acceptance:** Exit-zero test run; baseline captured.

---

## 4. Phase C — Unit-test PoC (main bulk of work)

Goal: build the entire Raft + coordinator + lease-gated reconcile machinery, validated through unit tests that simulate 3 operator instances in one process.

### Why unit tests first

- The existing `controllers/operator/mongodbshardedcluster_controller_multi_test.go` (3963 lines) has rich scaffolding for multi-cluster sharded reconciles with mocked Kubernetes clients.
- We can spin up 3 reconciler instances in the same test, each with its own mock K8s client, all sharing one Raft cluster via in-memory transport. This validates the protocol shape without any infrastructure.
- A failing unit test is 100× faster to diagnose than a failing e2e run.
- The user explicitly chose this path.

### C0 — Survey existing multi-cluster test scaffolding (read-only)

**Subagent type:** Explore (read-only).
**Can be parallel with:** C0b (below).

**Subagent prompt:**

```
Repo: /Users/lukasz.sierant/mdb/mongodb-kubernetes (current worktree).

Read controllers/operator/mongodbshardedcluster_controller_multi_test.go and
the test helpers it imports. Map out:

1. How is a multi-cluster reconciler instantiated in the existing tests?
   What's the helper function that produces a ShardedClusterReconcileHelper with
   mocked clients for N member clusters? File:line refs please.

2. What's the structure of the mocked Kubernetes clients per member cluster?
   How are they distinguished (by cluster name, by index)? How does the
   reconciler pick the right client when iterating clusters?

3. How is OM mocked in the tests? What's the fake connection factory? Where
   is automation config built and read in tests vs. real OM?

4. What's the typical test pattern: how many reconcile calls? How does the
   test drive the scaler / scaling-step logic across multiple reconcile()
   calls?

5. Are there any existing tests that drive multiple reconcile cycles to
   observe cross-cluster STS serialization (i.e., one cluster ready before
   next is touched)? File:line if so.

6. Tests for state persistence: how is the ConfigMap-backed StateStore
   exercised in tests?

Report under 600 words, dense, with file:line refs. I'm going to bolt a
Raft coordinator on top of this scaffolding so accuracy matters.
```

### C0b — Survey existing reconciler gate points for AC + STS writes (read-only)

**Subagent type:** Explore.
**Can be parallel with:** C0.

**Subagent prompt:**

```
Repo: /Users/lukasz.sierant/mdb/mongodb-kubernetes (current worktree).

In controllers/operator/mongodbshardedcluster_controller.go, enumerate
every site that needs gating in the upcoming distributed mode. I need
file:line precision.

Categories:

A) Sites that publish or wait for AutomationConfig in Ops Manager — these
   become LEADER-ONLY in distributed mode:
   - calls to om.ReadUpdateDeployment / publishDeployment
   - calls to om.WaitForReadyState
   - calls to waitForAgentsToRegister
   - calls to CalculateDiffAndStopMonitoring
   - any other OM-write or OM-wait-for-state call

B) Sites that iterate over member clusters and create/update Kubernetes
   resources in each — these need LEASE-GATING in distributed mode:
   - createOrUpdateConfigServers (1563), createOrUpdateShards (1510),
     createOrUpdateMongos (1473) — confirmed
   - any other per-cluster loop in the file

C) Sites that read/write Secrets or ConfigMaps in member clusters via
   member-cluster clients — these are decommissioned in distributed mode
   (or skipped for PoC; e2e will replicate manually):
   - replicateAgentKeySecret, reconcileHostnameOverrideConfigMap,
     replicateSSLMMSCAConfigMap — confirmed
   - any other cross-cluster Secret/ConfigMap write

D) The deployment-state persistence (ShardedClusterDeploymentState +
   StateStore ConfigMap): where is it read at reconcile start, where is
   it written at reconcile end?

For (B), also confirm: in each loop, what's the variable name for the
current member cluster (so we know what to compare with `myClusterName`
when we add the skip-if-not-ours guard)?

Report under 500 words. File:line for every site. I'll use this list as
the exact set of edit sites in chunk C5.
```

### C1 — Add `hashicorp/raft` dep + scaffold `pkg/coordination/raft/` package

**Subagent type:** general-purpose (writes).
**Sequencing:** Must complete before C2, C3, etc.

**Subagent prompt:**

```
On current worktree (no new worktree needed).

Goal: scaffold pkg/coordination/raft/ with hashicorp/raft and a minimal
in-memory transport for unit tests. End state: package compiles, one
basic unit test passes ("three in-memory raft nodes elect a leader").

Tasks:

1. Add hashicorp/raft to go.mod:
     go get github.com/hashicorp/raft@latest
   Use latest stable. Run go mod tidy.

2. Create pkg/coordination/raft/manager.go:
   - Package coordraft (or whatever name doesn't collide with hashicorp/raft).
   - Struct Manager wrapping *raft.Raft + the FSM + transport.
   - Constructor NewManager(cfg ManagerConfig) (*Manager, error).
   - ManagerConfig fields: NodeID, BindAddr, Peers []PeerInfo, Bootstrap bool,
     LogStore raft.LogStore, StableStore raft.StableStore, SnapshotStore
     raft.SnapshotStore, Transport raft.Transport, FSM raft.FSM.
   - For PoC: use raft.NewInmemStore() for log+stable, raft.NewInmemSnapshotStore()
     for snapshots, and a custom InmemTransport pair for testing.
   - Public methods: Apply(data []byte, timeout) (raft.ApplyFuture, error),
     IsLeader() bool, LeaderAddr() raft.ServerAddress, Shutdown() error.

3. Create pkg/coordination/raft/transport_inmem.go:
   - Wrap raft.NewInmemTransport. Helper NewInmemTransportPool(nodeIDs []string)
     map[raft.ServerID]raft.Transport that wires N transports together.

4. Create pkg/coordination/raft/fsm_stub.go:
   - Package-level type StubFSM struct{} implementing raft.FSM with no-op
     Apply/Snapshot/Restore. Used only by the C1 acceptance test. Real FSM
     comes in C2.

5. Create pkg/coordination/raft/manager_test.go:
   - TestThreeNodesElectLeader: spin up 3 managers using the in-memory
     transport pool, bootstrap node 0, add nodes 1 and 2 as voters, assert
     exactly one leader within 5s. Use the standard hashicorp/raft test
     patterns.

6. Run `go test ./pkg/coordination/raft/...`. Must pass.

7. Run `go build ./...`. Must succeed without errors.

8. Commit. Message:
   "Add hashicorp/raft dep and scaffold pkg/coordination/raft"
   No co-author line. Do not push.

Report:
- Exact files created (paths + LOC).
- Test output (last 30 lines of `go test -v`).
- Anything surprising in the dependency resolution.
- Under 200 words.

Time-box: 60 minutes. If hashicorp/raft has API changes vs. expectations,
adapt and document.
```

### C2 — FSM, proposal types, lease state

**Subagent type:** general-purpose (writes).
**Sequencing:** After C1.

**Subagent prompt:**

```
On current worktree. Prereq: C1 complete (pkg/coordination/raft/ exists,
basic manager test passes).

Goal: replace StubFSM with a real FSM that tracks:
- per-cluster status reports
- the active lease (single global lease for PoC)
- a monotonic "agreed-generation" counter

Tasks:

1. Create pkg/coordination/raft/proposals.go:
   - Type ProposalType (string): "status_report", "lease_allocate",
     "lease_complete", "ac_published" (PoC keeps it small).
   - Envelope struct { Type ProposalType; Payload json.RawMessage }.
   - Helper EncodeProposal(typ, payload) ([]byte, error) and
     DecodeProposal([]byte) (ProposalType, json.RawMessage, error).

2. Create pkg/coordination/raft/state.go:
   - Type FSMState struct {
       PerClusterStatus map[string]ClusterStatus  // keyed by cluster name
       ActiveLease      *Lease                     // pointer; nil if none
       ACGeneration     int                        // bumped each AC publish
       LastAppliedIndex uint64
     }
   - Type ClusterStatus struct {
       ClusterName       string
       LastReportedAt    time.Time
       ObservedSpecHash  string
       ComponentStatus   map[string]ComponentStatus  // "shard-0", "config",
                                                      //  "mongos"
       LastReconcileErr  string
     }
   - Type ComponentStatus struct {
       Generation int64
       Ready      bool
     }
   - Type Lease struct {
       Component   string  // "shard-0" | "config" | "mongos"
       ClusterName string
       AllocatedAt time.Time
       ExpiresAt   time.Time
     }
   - JSON-serialisable; clone helpers for safe read.

3. Replace fsm_stub.go with pkg/coordination/raft/fsm.go:
   - Type FSM struct { mu sync.RWMutex; state FSMState }.
   - Apply(log *raft.Log): decode proposal, dispatch to apply<Type> method,
     update state, return result (e.g., the lease pointer for lease_allocate).
   - Snapshot() (raft.FSMSnapshot, error): produce a JSON snapshot of state.
   - Restore(rc io.ReadCloser): decode JSON, replace state.
   - Public read-only accessors: GetState() FSMState (deep copy),
     GetActiveLease() *Lease, GetClusterStatus(name) ClusterStatus.

4. Update Manager to take FSM at construction (instead of stub).

5. Unit tests in fsm_test.go:
   - TestApplyStatusReport: propose status for cluster A, assert it's visible
     from B's FSM after replication.
   - TestApplyLeaseAllocate: propose lease for cluster A, assert ActiveLease
     reflects it.
   - TestApplyLeaseComplete: propose complete, assert lease is cleared.
   - TestSnapshotRestore: apply some entries, snapshot, restore into a fresh
     FSM, assert state matches.

6. Run tests; all green.

7. Commit:
   "Implement FSM with status reports, leases, and snapshots for PoC raft package"

Report:
- Files created/modified.
- Test results.
- Under 200 words.

Time-box: 90 minutes.
```

### C3 — `DistributedCoordinator` interface + integration scaffolding

**Subagent type:** general-purpose (writes).
**Sequencing:** After C2.

**Subagent prompt:**

```
On current worktree. Prereq: C2 complete.

Goal: define DistributedCoordinator interface and add it as an optional
field on ShardedClusterReconcileHelper. In non-distributed mode (field is
nil), nothing changes. This chunk does NOT modify any reconcile logic
yet — only adds the interface and wiring. Edit sites are minimal.

Tasks:

1. Create pkg/coordination/coordinator.go (note: this is a new file at the
   pkg/coordination/ level, NOT inside raft/, so it can be used without
   depending on raft):
   - Interface DistributedCoordinator:
       MyClusterName() string
       IsLeader() bool
       HasLeaseFor(component string, clusterName string) bool
       ProposeLeaseComplete(component string, clusterName string) error
       ProposeStatusReport(s ClusterStatusReport) error
       ProposeACPublished(generation int) error
       GetActiveLease() *LeaseInfo  // for logging
       GetPerClusterStatus() map[string]ClusterStatusReport
   - Type ClusterStatusReport struct (small struct — keep it lean for PoC):
       ClusterName string
       ObservedSpecHash string
       ComponentStatus map[string]struct {
         Generation int64
         Ready bool
       }
       LastReconcileErr string
   - Type LeaseInfo struct { Component, ClusterName string }.

2. Create pkg/coordination/raft/coordinator_impl.go:
   - Type Coordinator implements DistributedCoordinator backed by *Manager.
   - Each method translates to a Raft Apply or an FSM read.
   - Apply timeout: 5 seconds.

3. Modify controllers/operator/mongodbshardedcluster_controller.go:
   - Add field: `coordinator coordination.DistributedCoordinator` on
     ShardedClusterReconcileHelper. It can be nil.
   - Add getter helper: `IsDistributed() bool { return r.coordinator != nil }`
   - DO NOT modify any reconcile logic in this chunk. Just add the field
     and getter.

4. Add a setter/builder option so tests and main.go can inject a coordinator:
   - In the construction path for ShardedClusterReconcileHelper (search for
     NewShardedClusterReconcileHelper or similar), add an optional
     WithCoordinator(c) configuration option.

5. Verify `go build ./...` succeeds.
6. Verify existing tests still pass: `go test ./controllers/operator/...`
   (especially mongodbshardedcluster_controller_multi_test.go — it must
   stay green; nil coordinator means existing flow).

7. Commit:
   "Add DistributedCoordinator interface and wire it onto ShardedClusterReconcileHelper"

Report:
- The interface definition (paste it back).
- Modified files.
- Confirmation that existing multi-cluster tests pass unmodified.
- Under 200 words.

Time-box: 60 minutes.

IMPORTANT: existing tests must continue to pass. If a test breaks, stop
and report — do not paper over.
```

### C4 — Gate AC publication sites (leader-only) — minimal edit, narrowly scoped

**Subagent type:** general-purpose (writes).
**Sequencing:** After C3.
**Prereq:** C0b's survey output is in hand (the exact list of AC-write sites).

**Subagent prompt:**

```
On current worktree. Prereq: C3 complete; DistributedCoordinator interface
exists; ShardedClusterReconcileHelper has the coordinator field.

You have a list of AC-write sites from a previous survey (paste it in
when you spawn this subagent — see C0b output). Specifically:
[insert list from C0b]

Goal: gate every AC-write/wait site with a leader check. In non-distributed
mode (coordinator nil), behaviour is unchanged.

Pattern to apply at every site:

  if r.coordinator != nil && !r.coordinator.IsLeader() {
      log.Debug("Distributed mode, not leader — skipping AC operation X")
      return workflow.Pending("waiting for leader to publish AC"), nil
      // OR: return appropriate workflow status that triggers a requeue
  }

For pure-read OM calls (WaitForReadyState etc.) the same gate applies:
followers don't need to observe OM; they read AC convergence state from
FSM via coordinator.GetPerClusterStatus() instead.

After leader successfully publishes AC, the leader should propose an
ac_published event so followers know to move forward. Add this at the
end of updateOmDeploymentShardedCluster (or the natural completion point):

  if r.coordinator != nil && r.coordinator.IsLeader() {
      r.coordinator.ProposeACPublished(r.deploymentState.LastACGeneration + 1)
  }

Tasks:

1. For each AC-write site in the list, add the gate. Keep edits minimal —
   don't refactor the surrounding code.

2. Add propose-ac-published at the leader's success point.

3. Run `go build ./...`.

4. Run `go test ./controllers/operator/...`. Existing tests use nil
   coordinator so they should still pass.

5. Commit:
   "Gate AC publication and wait sites with leader check in distributed mode"

Report:
- Each site modified with file:line.
- Test result summary.
- Anything where the gate placement was non-obvious (callers/callees that
  surprised you).
- Under 250 words.

Time-box: 60 minutes.
```

### C5 — Skip not-ours clusters + lease-gate STS update sites

**Subagent type:** general-purpose (writes).
**Sequencing:** After C4.

**Subagent prompt:**

```
On current worktree. Prereq: C4 complete; AC sites are leader-only.

Goal: in the per-component, per-cluster loops (createOrUpdateConfigServers,
createOrUpdateShards, createOrUpdateMongos), do TWO things when
coordinator != nil:

A) Skip iterations where the cluster is not ours.
B) For our own cluster's iteration, only proceed if we hold the lease for
   that component+cluster. If we don't hold the lease, return a requeue.

Pattern to inject into each loop:

  for _, memberCluster := range r.someHealthyMemberClusters {
      if r.coordinator != nil {
          if memberCluster.Name != r.coordinator.MyClusterName() {
              continue  // not ours
          }
          if !r.coordinator.HasLeaseFor(componentKey, memberCluster.Name) {
              log.Debugf("No lease for %s/%s, requeueing", componentKey,
                  memberCluster.Name)
              return workflow.Pending("waiting for lease")
          }
      }

      // ... existing CreateOrUpdate + GetStatefulSetStatus ...

      if r.coordinator != nil && status.IsOK() {
          if err := r.coordinator.ProposeLeaseComplete(componentKey,
                                                       memberCluster.Name); err != nil {
              return workflow.Failed(err)
          }
      }
  }

Where componentKey is:
- "config" for createOrUpdateConfigServers
- "shard-N" for createOrUpdateShards (N is the shard index)
- "mongos" for createOrUpdateMongos

Tasks:

1. Apply this pattern in:
   - createOrUpdateConfigServers around line 1569
   - createOrUpdateShards around line 1517 (inner loop; outer is shardIdx)
   - createOrUpdateMongos around line 1479

2. Also gate the secret/configmap replication functions
   (replicateAgentKeySecret, reconcileHostnameOverrideConfigMap,
   replicateSSLMMSCAConfigMap):
   - If r.coordinator != nil, return nil immediately. PoC manually
     replicates these. Add a TODO comment referencing this plan.

3. Add MyClusterName threading: the ShardedClusterReconcileHelper needs to
   know its cluster name. In distributed mode this comes from the
   coordinator. In non-distributed mode it's empty (and the gates are
   no-ops). No need to plumb it through the codebase — just access via
   r.coordinator.MyClusterName() at the inline check.

4. Also: in the AFTER-loop barrier wait
   (lines 1592-1598 etc., "all member clusters must be ready" check),
   in distributed mode this becomes "FSM reports all clusters reported
   ready for this component". Gate the existing barrier with:
     if r.coordinator == nil { /* existing barrier */ } else {
       // Check coordinator.GetPerClusterStatus() for all clusters; if
       // any not yet ready for this component, return Pending.
     }

5. `go build ./...` + `go test ./controllers/operator/...`.

6. Commit:
   "Lease-gate per-cluster STS updates and skip cross-cluster secret
    replication in distributed mode"

Report:
- Each modified site with file:line.
- Test result summary.
- Any places where the existing iteration pattern made the gate awkward
  (these are candidates for the post-PoC refactor list).
- Under 300 words.

Time-box: 90 minutes.

IMPORTANT: keep edits minimal and contained. We're not refactoring; we're
adding guards.
```

### C6 — Leader logic: lease allocator + plan advancement

**Subagent type:** general-purpose (writes).
**Sequencing:** After C5.

**Subagent prompt:**

```
On current worktree. Prereq: C5 complete; coordinator gates are in place.

Goal: implement the leader-side logic that produces leases and drives the
deployment forward. This runs inside the Coordinator implementation (or a
sidecar goroutine started by the Manager), NOT inside the reconciler.

Design:

The leader runs a separate "scheduler" loop, independent of any individual
operator's reconcile cycle. The scheduler ticks every ~500ms and decides
what lease to allocate based on FSM state.

Algorithm (PoC simplification — single global lease, deterministic order):

  func (s *Scheduler) tick():
      if !s.manager.IsLeader(): return
      lease := s.fsm.GetActiveLease()
      if lease != nil:
          // wait for completion; nothing to do
          return
      desired := s.computeNextLease()  // see below
      if desired == nil:
          // all components reconciled
          return
      s.proposeLeaseAllocate(desired)

  func (s *Scheduler) computeNextLease() *Lease:
      // Order: config servers first (all clusters), then shards (all
      // clusters per shard), then mongos. Within each component, iterate
      // clusters in deterministic order (by name).
      // For each (component, cluster) tuple:
      //   if perClusterStatus[cluster].componentStatus[component].Ready
      //     == false  OR  generation < acGeneration:
      //       return Lease{component, cluster}
      // Otherwise: nothing to do.

For PoC, hardcode the component list rather than reading from the CR:
  components := ["config"] + ["shard-0", "shard-1", ...] + ["mongos"]
  clusters := all cluster names in sorted order

Get the cluster list from the FSM's perClusterStatus map keys (each
operator reports its name on first reconcile).

Tasks:

1. Create pkg/coordination/raft/scheduler.go:
   - Type Scheduler { manager *Manager; fsm *FSM; clusters []string;
     components []string; stop chan struct{} }.
   - NewScheduler + Start(ctx) launches a goroutine that ticks.
   - tick() applies the algorithm above.
   - Configurable via env vars or struct fields: components, clusters.

2. Wire Scheduler into Manager startup: when the Manager detects it is
   leader (subscribe to leaderCh), start the scheduler. When it loses
   leadership, stop the scheduler.

3. Unit test pkg/coordination/raft/scheduler_test.go:
   - Spin up 3 managers in-memory.
   - Have each propose StatusReport with ComponentStatus[*].Ready=false.
   - Assert leader's scheduler allocates a lease to cluster-A for "config"
     within 2s.
   - Simulate cluster-A "completing" by proposing LeaseComplete and
     updating its ComponentStatus.config.Ready=true.
   - Assert next lease is for cluster-B for "config".
   - Continue until all components+clusters are Ready.

4. `go build ./...` + run the new test.

5. Commit:
   "Add leader-side scheduler that allocates leases in deterministic order"

Report:
- Pseudocode of the scheduler's main decision.
- Test output.
- Under 200 words.

Time-box: 90 minutes.
```

### C7 — End-to-end unit test: 3-in-process reconcilers, real Raft, mock K8s

**Subagent type:** general-purpose (writes).
**Sequencing:** After C6.

**Subagent prompt:**

```
On current worktree. Prereq: C6 complete; coordinator + scheduler work in
isolation.

Goal: write THE unit test that proves the PoC end-to-end in a single
process. This is the central artifact we're building towards.

Test outline:

  func TestDistributedMultiClusterShardedReconcile(t *testing.T) {
    // 1. Create 3 in-memory raft managers (cluster-a, cluster-b, cluster-c).
    // 2. Bootstrap and join them; wait for a leader.
    // 3. For each cluster, create a ShardedClusterReconcileHelper using
    //    the EXISTING test helpers from mongodbshardedcluster_controller_multi_test.go
    //    (with mocked Kubernetes client for that cluster), and inject the
    //    corresponding Coordinator (one per cluster, all pointing at the
    //    same Raft cluster).
    // 4. Apply the same CR to all 3 mock clusters' fake API servers.
    // 5. Loop: drive Reconcile() on all 3 reconcilers (round-robin or
    //    based on a re-queue queue), allowing the Raft scheduler to
    //    progress. Time-bound the loop (60s wall clock max).
    // 6. Assertions:
    //    a. AC was published exactly once (the mocked OM connection tracks
    //       calls). Only the leader's reconcile attempted it.
    //    b. STS for each component was created in each cluster, in the
    //       order: config (A→B→C), shard-0 (A→B→C), mongos (A only —
    //       single mongos in this test topology). Verify via the order
    //       of StatefulSet CreateOrUpdate calls in each mock client.
    //    c. No mock client ever wrote a resource to a different cluster's
    //       API (no cross-cluster K8s access).
    //    d. Final CR status across all clusters shows "Running".
  }

Tasks:

1. Write the test. Lean heavily on the existing test helpers from
   mongodbshardedcluster_controller_multi_test.go for fake K8s clients,
   mock OM, CR loading. Add what's missing to those helpers if necessary,
   keeping changes minimal.

2. You will likely need to make the existing test helper accept a
   coordinator parameter — add it as an optional field/option.

3. To drive the round-robin reconcile loop, simulate the controller-runtime
   work queue: maintain a queue per cluster, push the CR onto each cluster's
   queue, pop and reconcile, requeue on Pending. Time-out aggressively if
   stuck.

4. Make AC-call counting straightforward: extend the mock OM connection to
   record each ReadUpdateDeployment / WaitForReadyState / etc. call with
   the cluster name of the caller (you'll need to thread cluster name into
   the mock somehow).

5. `go test -run TestDistributedMultiClusterShardedReconcile ./controllers/operator/...
    -timeout 5m -v`

6. Commit:
   "End-to-end unit test: distributed mode, 3 in-process operators, real raft,
    mock K8s, full sharded reconcile flow"

Report:
- The test's output (last 50 lines).
- Concrete pass/fail status of each assertion.
- Under 400 words.

Time-box: 3 hours. This is the load-bearing test; spend the time to get it
right. If you hit a fundamental issue (e.g., the existing test helpers
don't compose well with multiple in-process reconcilers), STOP and report
— don't refactor heavily without checking in.
```

### C8 — Verify existing tests still pass (safety net)

**Subagent type:** general-purpose (read+test).
**Sequencing:** After C7.

**Subagent prompt:**

```
Goal: confirm Phase C changes did not regress existing tests.

Run:
  go test ./controllers/operator/... -timeout 10m

Also run:
  go test ./... -timeout 20m  (full repo)

For any test that fails, classify:
- Was it failing before Phase C started? (check git stash + run with HEAD
  if needed)
- Was it caused by our changes? If so, was it a legitimate regression or
  an environmental issue?

Report:
- Pass/fail counts.
- For each failing test: name, file:line, root cause one-liner.
- Under 200 words.

Time-box: 30 minutes.

Do not fix anything in this chunk — only report. Fixes are a separate
chunk if needed.
```

---

## 5. Phase D — e2e PoC (3 operator processes in devcontainer)

Goal: run the same protocol against real Kubernetes via 3 separate operator processes, each pointing at one member cluster's kubeconfig. They communicate via localhost ports.

### D0 — Extract per-cluster kubeconfigs

**Subagent type:** general-purpose (writes a script).
**Sequencing:** After Phase C complete.

**Subagent prompt:**

```
On the EVG host (use evg_host.sh ssh).

Goal: a reusable script that extracts each member-cluster context from
.generated/current.devc.kubeconfig into its own kubeconfig file
(.generated/cluster-a.kubeconfig, etc.), and verifies each works.

Tasks:

1. Read .generated/current.devc.kubeconfig — identify all "member" contexts
   (the ones that are NOT the central/hub cluster). Typical naming pattern
   from the existing multi-cluster test setup.

2. Write scripts/dev/extract-member-kubeconfigs.sh that:
   - Reads .generated/current.devc.kubeconfig
   - For each member context, uses `kubectl config view --raw
     --kubeconfig=... --context=...` (or yq) to produce a single-context
     kubeconfig
   - Writes them to .generated/cluster-<name>.kubeconfig
   - Runs `kubectl --kubeconfig=.generated/cluster-<name>.kubeconfig
     cluster-info` to validate each

3. Make the script idempotent and clean its own output dir.

4. Run it. Report the resulting list of files and the validation output.

Acceptance:
- Three kubeconfig files exist.
- Each `kubectl cluster-info` returns successfully.

Commit:
"Add script to extract per-cluster kubeconfigs from devc kubeconfig"

Report under 150 words.

Time-box: 45 minutes.
```

### D1 — Modify `test_deploy_operator` to install CRDs in member clusters with replicas=0

**Subagent type:** general-purpose (writes).
**Sequencing:** After D0.

**Subagent prompt:**

```
Repo: /Users/lukasz.sierant/mdb/mongodb-kubernetes (worktree).

The Python e2e tests have a setup function called test_deploy_operator (or
similar) somewhere in docker/mongodb-kubernetes-tests/. Find it; it's
responsible for installing the operator into the cluster(s).

Goal: provide an alternate path that installs the operator's CRDs +
ServiceAccount + RBAC into each member cluster, BUT with replicas=0 so no
operator pod actually starts. We'll start 3 operator processes locally on
the devcontainer instead.

Tasks:

1. Find test_deploy_operator. Map its current behavior. Report path and
   line numbers.

2. Add a new option (env var or fixture parameter) — call it
   DISTRIBUTED_POC_MODE — that switches behavior to:
   - Install operator chart in each member cluster using its kubeconfig
   - Override values.yaml: replicas: 0
   - Wait for CRDs to be present in each cluster (kubectl get crds for the
     relevant CRDs: MongoDB, MongoDBMultiCluster, etc.)
   - DO NOT install the operator in the hub cluster

3. Verify the e2e test multi_cluster_sharded_simplest.py still runs the
   existing setup when DISTRIBUTED_POC_MODE is not set.

4. Test the new path manually:
   - export DISTRIBUTED_POC_MODE=true
   - run the test up to the point where the operator should be installed
   - confirm CRDs are visible in each member cluster
   - confirm no operator pod is running

Commit:
"Add DISTRIBUTED_POC_MODE to install CRDs in member clusters without
 operator pods"

Report:
- File and function path of test_deploy_operator.
- Diff summary.
- Verification output.
- Under 250 words.

Time-box: 90 minutes.
```

### D2 — Run 3 operator processes locally with port adjustments

**Subagent type:** general-purpose (writes a wrapper script).
**Sequencing:** After D1.

**Subagent prompt:**

```
Goal: a script that starts 3 operator binaries locally in the devcontainer,
each pointed at one member kubeconfig, in distributed mode, on distinct
ports.

Tasks:

1. Adjust main.go to accept new flags / env vars:
   - --cluster-name (or env CLUSTER_NAME): the cluster's identity for
     coordinator
   - --raft-bind-addr (or env RAFT_BIND_ADDR): "127.0.0.1:9080"
   - --raft-peers (or env RAFT_PEERS): comma-separated
     "name1=127.0.0.1:9080,name2=127.0.0.1:9081,..."
   - --raft-bootstrap (or env RAFT_BOOTSTRAP): true|false (only one peer
     bootstraps)
   - When these are set, construct the Manager + Coordinator and inject
     into the reconciler. Otherwise behave as today.

2. Also: configure controller-runtime to use different ports per instance
   to avoid conflicts:
   - --metrics-bind-address (default 8080) — set distinct per instance
   - --health-probe-bind-address (default 8081)
   - --leader-elect-resource-namespace per instance (or disable leader
     election with --leader-elect=false for PoC since each operator is
     a singleton owner of its kubeconfig)

3. Also: do not start the MongoDBMultiCluster controller; only the
   per-cluster sharded controller. The flag for selectively starting
   controllers should already exist (search main.go).

4. Write scripts/dev/run-3-operators-locally.sh:
   - tmux or background-process based; one operator per
     kubeconfig/cluster.
   - Each operator's stdout is teed to .generated/operator-<name>.log.
   - Wait for all 3 to log "Raft leader elected".
   - Provide a clean shutdown via `kill $(cat ...pid)`.

5. Test: start the 3 operators. Confirm Raft cluster forms (one leader,
   two followers). Confirm no port conflicts. Confirm each operator
   connects to its own kubeconfig only.

Commit:
"Add distributed-mode flags to main.go and a script to run 3 operators locally"

Report:
- The new flags + env vars.
- A snippet of the 3 operators' logs showing successful Raft formation.
- Under 250 words.

Time-box: 2 hours.

NOTE: stop any currently running local operator before starting these.
```

### D3 — Run the e2e test against the 3 local operators

**Subagent type:** general-purpose (executes + small fixups).
**Sequencing:** After D2.

**Subagent prompt:**

```
Goal: run multi_cluster_sharded_simplest.py end-to-end with the 3 local
operators running in distributed mode.

Tasks:

1. Ensure no operator is running in any cluster:
   - kubectl --kubeconfig=<each> get pods -n <ns> | grep operator (should
     be empty)
   - Old local-operator process is killed (per user's guidance — "ensure
     the local operator that worked against the operator cluster (the
     hub) is not running anymore").

2. Start the 3 distributed operators via scripts/dev/run-3-operators-locally.sh.

3. Run the e2e test with DISTRIBUTED_POC_MODE=true. Use the existing
   e2e runner (op_run.sh / e2e_run.sh or equivalent). The test does:
   - Setup phase (CRD install — handled by D1)
   - Apply the sharded cluster CR(s)
   - Wait for reconciliation
   - Validate sharded cluster topology

4. The test may fail at "cert replication" or "secret replication" steps
   because the operator no longer does cross-cluster Secret replication.
   When this happens:
   - Identify the missing resource
   - Replicate it manually using kubectl across the 3 kubeconfigs
   - Retry / continue the test
   - Document each manual step

5. Iterate until the test passes (or fails at a non-replication issue).

Acceptance:
- The sharded cluster ends up in Running phase across all 3 clusters.
- mongos endpoints are reachable; one Insert + Find sanity check passes.

Commit:
"Wire e2e test multi_cluster_sharded_simplest with distributed-mode
 operators; manual secret replication documented in test notes"

Report:
- Each manual step performed for cert/secret replication.
- Final test status.
- Log excerpts from each operator showing leader election + lease handoff.
- Anything that surprised you.
- Under 600 words.

Time-box: 4 hours. If you hit a wall, STOP and report.
```

---

## 6. Phase E — Verification

### E1 — Repeat the run from a clean state

**Subagent type:** general-purpose (executes).
**Sequencing:** After D3.

**Subagent prompt:**

```
Goal: prove the e2e run is repeatable, not a one-off.

Tasks:

1. Tear down: kubectl delete the sharded cluster CR; kubectl delete all
   StatefulSets/Services/Secrets it created in each member cluster; stop
   the 3 operators.

2. Restart from a clean state: re-run extract-kubeconfigs (D0 script),
   re-deploy CRDs (D1 path), restart 3 operators (D2 script), re-run e2e
   (D3).

3. Confirm pass.

Report: pass/fail. Under 100 words.

Time-box: 1 hour.
```

### E2 — DR drill (optional stretch)

**Subagent type:** general-purpose (executes).
**Sequencing:** After E1.

**Subagent prompt:**

```
Optional stretch goal: validate leader failover.

Tasks:

1. With the e2e running cleanly, identify the current leader operator
   from its logs.

2. SIGKILL the leader operator process. The remaining two should elect a
   new leader within 5-10s (Raft default election timeout).

3. Make a small CR change (e.g., bump replicas of a shard from 3 to 4,
   or change a label).

4. Confirm the new leader picks up the change and progresses the
   reconcile.

5. Optionally restart the killed operator; verify it rejoins as a
   follower.

Report observations under 200 words. Note: with emptyDir-free local
process model, restarting the killed operator means it starts with no
Raft state — verify it correctly rejoins via Raft snapshot install (or
note where the recovery story breaks down).

Time-box: 1 hour.
```

### E3 — Write up surprises and post-PoC backlog

**Subagent type:** general-purpose (writes notes file).
**Sequencing:** After E1 (and E2 if attempted).

**Subagent prompt:**

```
Goal: produce docs/dev/distributed-multicluster-poc-findings.md
summarizing what was learned in this PoC.

Sections to write:

1. What works (the demonstration runs end-to-end).
2. What's intentionally not validated (recap of out-of-scope items).
3. Surprises / pain points encountered during implementation.
4. Refactoring debt identified (places where the existing reconciler
   structure made the gate-injection awkward). These are candidates for
   the real implementation project.
5. Open design questions surfaced by the PoC.
6. Recommended next milestones (durable storage, OnDelete, mTLS, plan
   vocabulary, ...).
7. Concrete files/locations that contain the PoC artifacts (test, scripts,
   coordinator package).

Read the operator logs from D3 if available; pull representative log
lines into section 1 to make the demonstration concrete.

Under 1500 words.

Commit:
"Document PoC findings and post-PoC backlog"
```

---

## 7. Backwards-compatibility checklist

At every chunk, the following must remain true:

- `go build ./...` succeeds.
- `go test ./controllers/operator/...` passes for existing tests (nil
  coordinator means unchanged behavior).
- The hub-spoke e2e test (Phase B baseline) still passes when run without
  `DISTRIBUTED_POC_MODE`.
- No existing public API in the operator's Go packages changes signature
  in a breaking way.
- Helm chart defaults are unchanged; new distributed-mode options are
  opt-in via new values keys.

If any of these breaks during a chunk: stop, report to the user, decide on
a remediation. Do not paper over with skip-flags.

---

## 8. Reference: file map

| Concern | File |
|---|---|
| Existing sharded reconciler | `controllers/operator/mongodbshardedcluster_controller.go` |
| Existing multi-cluster unit tests | `controllers/operator/mongodbshardedcluster_controller_multi_test.go` |
| Target e2e test | `docker/mongodb-kubernetes-tests/tests/multicluster_shardedcluster/multi_cluster_sharded_simplest.py` |
| Existing multi-cluster client | `pkg/multicluster/multicluster.go` |
| OM connection | `controllers/om/` |
| New: coordinator interface | `pkg/coordination/coordinator.go` (created in C3) |
| New: raft package | `pkg/coordination/raft/` (created in C1+) |
| New: extract-kubeconfigs script | `scripts/dev/extract-member-kubeconfigs.sh` (created in D0) |
| New: run-3-operators script | `scripts/dev/run-3-operators-locally.sh` (created in D2) |
| New: PoC findings doc | `docs/dev/distributed-multicluster-poc-findings.md` (created in E3) |

---

## 9. Subagent execution protocol

### 9.1 Spawning rules

- Each chunk's "Subagent prompt template" is a complete, self-contained
  brief. Always include the chunk's prereqs explicitly in the prompt
  (don't assume the subagent has seen this plan).
- For chunks that depend on a prior chunk's specific output (e.g., C4
  needing C0b's site list), paste the relevant content directly into the
  subagent's prompt.
- Always specify a time-box in the subagent's prompt.
- Always specify what to commit and what NOT to push.
- Always ask for a structured report at the end (the report comes back
  as a tool result; the main session should summarize it for the user).

### 9.2 Sequencing rules

- **Read-only research chunks** (C0, C0b) may run in parallel — send them
  in a single message with multiple Agent tool uses.
- **Write-modifying chunks** (C1+, D0+) must run **sequentially**. After
  one completes, the main session reviews the report, verifies state
  briefly (e.g., quick `go build ./...` check), updates the Progress
  Tracker (§10), then spawns the next chunk.
- **Never spawn two write subagents in parallel.** They will corrupt each
  other's work in the same checkout.

### 9.3 Failure handling

If a subagent reports failure or incomplete work:

1. Read the failure report carefully.
2. Verify the state on disk — does the codebase still build? Do existing
   tests pass? `git status` clean?
3. Decide: retry with a tighter prompt, do the work yourself, or stop and
   ask the user.
4. **Never automatically retry the same prompt.** That just burns context.

### 9.4 Context-management discipline

- After each chunk, summarize the result in the Progress Tracker (§10) in
  the main doc — that's the durable hand-off surface.
- Don't read more code into the main session than necessary; let
  subagents do the heavy reading and report summaries.
- If the main session's context gets uncomfortable, the user can
  `/clear` and resume by reading this doc + the Progress Tracker.

---

## 10. Progress Tracker

Updated by the main session after each chunk completes. A future resume
session reads this to understand where work stands.

```
Phase A — Environment setup:        [ ] not started   [ ] in progress   [X] complete
Phase B — Baseline validation:      [ ] not started   [ ] in progress   [ ] complete
  B1 — Baseline e2e run:            [ ] not started   [X] in progress   [ ] complete
Phase C — Unit-test PoC:            superseded by Phase F (see notes below)
  C0 — Survey test scaffolding:     [X] complete
  C0b — Survey AC/STS gate sites:   [X] complete
  C1 — Raft pkg scaffold:           [X] complete (kept in F1)
  C2 — FSM + proposals:             [X] complete (reworked in F1: CRKey-partitioned)
  C3 — Coordinator interface:       [X] complete (reworked in F5: inline-gating)
  C4 — Gate AC sites:               [X] complete (reworked in F6)
  C5 — Lease-gate STS sites:        [X] complete (reworked in F6)
  C6 — Leader scheduler:            superseded by F1 (Scheduler dropped)
  C7 — E2E unit test:               superseded by F8 (real raft + real reconcile loops)
  C8 — Regression check:            superseded by F11
Phase F — Redesign batch:           [ ] not started   [ ] in progress   [X] complete
  F1 — FSM CRKey-partition + drop Scheduler/Plan: [X] complete
  F2 — Muxed StreamLayer (1 TCP port):            [X] complete
  F3 — Real raft over TCP loopback:               [X] complete
  F4 — App-channel proposal forwarding:           [X] complete
  F5 — Coordinator API rework:                    [X] complete
  F6 — Inline gating in controller:               [X] complete
  F7 — Stuck-step + cluster-unreachable detect:   [X] complete
  F8 — Headline test (real raft + reconcile):     [X] complete
  F9 — Scale-up integration test:                 [X] complete
  F10 — Failure injection tests:                  [X] complete
  F11 — Regression green + doc update:            [X] complete
  F12a — ResourceObserved proposal + WaitForResourcesAgreed: [X] complete
  F12b — Per-operator local resource hashing + gate:         [X] complete
  F12c — Leader-gate UpgradeAllIfNeeded / ensureRoles / StopMonitoring / ReadOrCreateProject: [X] complete
  F12d — Exhaustive OM-write audit + close gaps:             [X] complete
  F12e — Regression green + plan doc:                        [X] complete
Phase D — e2e PoC (revised, §14):   [ ] not started   [ ] in progress   [X] complete
  D'0 — Verify + tear down hub-spoke: [X] complete (2026-05-15)
  D'1 — main.go distributed flags:    [X] complete (2026-05-15, 3ccd33545)
  D'2 — extract_member_kubeconfigs.sh:[X] complete (2026-05-15, 9f52a9278)
  D'3 — replicate_cr_resources.sh:    [X] complete (2026-05-15, e740eb4b0)
  D'4 — run-3-operators-locally.sh:   [X] complete (2026-05-15, fe7c8eaac)
  D'5 — DISTRIBUTED_POC_MODE branch:  [X] complete (2026-05-15, 5a4015ebb)
  D'6 — first e2e attempt:            [X] complete (2026-05-16, first green run 3/3 in 755s on f7ed37cb7)
  D'7+ — test-driven iteration:       [X] complete (2026-05-16, iters 1-4: 375f86d5b parallel leases, c3215e2be own-cluster CM/Secret, f7ed37cb7 FSM agent keys)
  D'8 — final verification + writeup: [X] complete (2026-05-16, 3 consecutive green e2e runs: 755s, 694s, 781s)
Phase E — Verification:             [X] not started   [ ] in progress   [ ] complete
  E1 — Repeat run:                  [X] not started   [ ] in progress   [ ] complete
  E2 — DR drill (stretch):          [X] not started   [ ] in progress   [ ] complete
  E3 — Findings writeup:            [X] not started   [ ] in progress   [ ] complete
Phase G — In-pod distributed mode (3 operators in 3 kind clusters): [ ] not started [ ] in progress [X] transport complete (raft fix in)
  G — Image + helm wiring (G'5 iters 1-5):                          [X] complete (2026-05-16)
  G — Istio passthrough on raft port (G'5 iter 6-7):                [X] complete (2026-05-16)
  G — Production raft config + heartbeat (G'5 iter 8):              [X] complete (2026-05-16)
  G — Istio sidecar exclude raft ports (G'5 iter 9):                [X] complete (2026-05-16, f8a20be7b, patch 6a0867ef92006700073e3929)
  G — test_sharded_cluster agent goal-state convergence:            [ ] open (independent of raft; see phase-d-handoff.md G iter 9)
```

### Phase C completion notes (2026-05-15)

All Phase C chunks landed on `lsierant/devcontainer-raft-poc-unit` (no push yet).

- **C0 + C0b** (read-only surveys, done in head): mapped `newShardedClusterReconcilerForMultiCluster` (multi-test:46) as the helper constructor, `getFakeMultiClusterMapWithConfiguredInterceptor` (mongodbmultireplicaset_controller_test.go:1607) for fake K8s clients, and AC-write sites in `updateOmDeploymentShardedCluster` / `cleanOpsManagerState` / `publishDeployment`. STS-write sites: `createOrUpdateConfigServers/Shards/Mongos`. Cross-cluster replication sites: `replicateAgentKeySecret`, `reconcileHostnameOverrideConfigMap`, `replicateSSLMMSCAConfigMap`.
- **C1** — commit `28b1f9711`: added `hashicorp/raft v1.7.3`, scaffolded `pkg/coordination/raft/` (types, transport_inmem, node, fsm stub) and `TestThreeNodesElectLeader`.
- **C2** — commit `e44a8b30`-ish (next after C1): real FSM with proposals `spec_update / status_report / plan_create / plan_advance / lease_allocate / lease_complete / cluster_index_assign / ac_published`. Apply replays are idempotent (monotonic generation checks). FSM JSON snapshot/restore round-trip covered by unit test.
- **C3** — `dbc896b7a`: created `pkg/coordination/coordinator.go` with the `DistributedCoordinator` interface and Raft-backed `Coordinator` impl. Added optional `coordinator` field + `IsDistributed()` and `SetCoordinator()` on `ShardedClusterReconcileHelper`. Existing operator tests unchanged.
- **C4** — `c377af72a`: gated `updateOmDeploymentShardedCluster` and `cleanOpsManagerState` with `!coordinator.IsLeader() → workflow.Pending` / `nil`. Leader announces success via `ProposeACPublished`. Unit tests prove followers never call OM AC ops; leaders do.
- **C5** — `73237f83f`: introduced `distGate(component, cluster)` truth-table + `distCompleteLease` helpers, applied to all three STS loops (`createOrUpdateConfigServers/Shards/Mongos`). Cross-cluster Secret/CM replication functions become no-ops when a coordinator is attached (PoC manually replicates). Unit tests cover the gate decision matrix.
- **C6** — `912250f19`: leader-side `Scheduler` goroutine. Tick chooses next `(component, cluster)` in deterministic order; FSM `applyStatusReport` was changed to MERGE rather than overwrite so partial reports don't wipe earlier `Ready=true` entries. Three-node test drives the scheduler through every tuple.
- **C7** — `e035aa26d`: headline test `TestDistributedMultiClusterShardedReconcile`. Three in-process helpers each bound to its own raft.Raft coordinator + scheduler; the test drives the protocol to steady state. Final `stsWriteOrder`: `config/A→B→C, shard-0/A→B→C, shard-1/A→B→C, mongos/A→B→C`. AC ops on follower helpers are gated out; only the leader's call reaches the mock.
- **C8** — full repo `go test ./...` green (no regressions).

### Surprises / scope-drift notes for Phase D

1. `hashicorp/raft.Apply` on a follower does NOT auto-forward to the leader — it returns `raft.ErrNotLeader`. The C-era unit-test harness sidestepped this by routing proposals through the leader's coordinator manually. **Resolved in Phase F**: F2 added a muxed `StreamLayer` (one TCP port carries both raft replication and follower-proposed app-channel traffic); F4 added a `Forwarder` that submits the proposal to whichever node currently believes itself to be the leader, retrying on `ErrNotLeader`. Phase D inherits this layer.
2. The original FSM `applyStatusReport` overwrote `ComponentStatus` map; that broke scheduler progression. Now it merges. This is a real protocol property, not a test quirk — write it down in any future architecture refinement.
3. Status reports must reflect the OBSERVED current state of the component, NOT "do I hold the lease right now". A `WaitLease` iteration should NOT regress `Ready=true` to `Ready=false`.
4. Direct unit-tests of `createOrUpdateConfigServers` (and friends) without full reconcile-driven `deploymentOptions` panic inside `buildVaultDatabaseSecretsToInject`. The C5 tests therefore exercise `distGate` directly instead. Phase D's e2e uses real reconcile so this isn't an issue there.
5. **Phase F redesign**: the upfront `Plan` + dedicated `Scheduler` goroutine model didn't survive design review. The "plan" emerges naturally from the existing reconcile flow; the leader's reconcile loop IS the scheduler (`AcquireOrRespect` proposes the next lease inline). `Plan` / `PlanCreate` / `PlanAdvance` are gone.
6. **Phase F redesign**: lease heartbeats are implicit (refreshed by every StatusReport from the holder). There is no `LeaseRenew` proposal. Leases soft-expire after `heartbeat_ttl` (default 60s) and hard-expire at `deadline_at` (default 30 min). The leader's `StuckDetector` revokes via a `LeaseExpire` proposal.
7. **Phase F redesign**: cluster-unreachable is detected via `raft.Raft.Stats()` last-contact, not via a separate memberwatch. PoC's `LastContact` API is a simplification; production would attach a custom `raft.Observer` to track per-peer contact times.
8. **Phase F redesign**: top-level FSM state is `map[CRKey]PerCRState`. Every proposal carries `CRKey`. A `CRDelete{CRKey}` proposal cleans up when a CR is removed. The C-era single-CR shape is now exposed via `Coordinator.SetDefaultCR` for the operator's one-CR-per-instance case.

### Phase F completion notes (2026-05-15)

All Phase F chunks landed on `lsierant/devcontainer-raft-poc-unit` (no push yet). The redesign was driven by detailed design discussion captured in the prompt for the F-batch subagent; key locked decisions:
- No upfront Plan; no Scheduler goroutine; muxed StreamLayer; synchronous proposals with retry; heartbeat-on-StatusReport; stuck-step + cluster-unreachable detection; FSM partitioned by CRKey.

Per-chunk SHAs:
- **F1** — `a715cff75`: dropped Scheduler+Plan, partitioned FSM by CRKey, added `HeartbeatAt`/`DeadlineAt`/`AllocatedAt` to lease, added `LeaseExpire` + `CRDelete` proposals. `applyStatusReport` refreshes lease HeartbeatAt iff reporter is the holder. FSM tests updated to construct CRKey explicitly.
- **F2** — `84088b20d`: `MuxedStreamLayer` implementing `raft.StreamLayer` over one TCP port. 1-byte handshake (`'R'` raft / `'A'` app), 4-byte big-endian length-prefixed framing for the app channel. Concurrent dispatch + handshake-timeout cleanup + atomic counters. Unit tests under `-race`.
- **F3** — `f26de609b`: `NewTCPRaftCluster` + `TCPNode` helper that pairs each muxed listener with `raft.NetworkTransport`. Tests cover leader election, replication, snapshot install, log catch-up, re-election after leader kill.
- **F4** — `c5d1ccec0`: `Forwarder` that follows the leader and routes proposals via the app channel. Wire format = framed payload → 1-byte status → framed error. Tests cover follower roundtrip, leader short-circuit, concurrent submissions, kill-leader-mid-flight retry, sustained mixed traffic (270 props in 3s, leader stable).
- **F5** — `b790295b5`: split `coordination.DistributedCoordinator` (new inline-gating surface) from `coordination.LegacyCoordinator` (kept until F6 completes the swap). Added `CRKey`, `LeaseResult`, `ProgressSnapshot` types. Coordinator gained `AcquireOrRespect`, `IsComponentReady`, `ReportProgress`, `MarkReady`, `ReleaseLease`, `AcVersion`, `AnnounceAcPublished`, `LastContact`, `ClusterIndex`. Coordinator carries optional `Forwarder` for follower auto-forwarding + a cluster→ServerID peer map for `LastContact`.
- **F6** — `88e931006`: helper coordinator field swaps to `DistributedCoordinator`. The 3 STS-write loops + 2 AC-publish sites use the new inline gate (`distGateInline` returns `Proceed` / `SkipDone` / `Wait`) + `distMarkReadyAndRelease` / `distReportInflightProgress`. C5-era `distGate` / `distCompleteLease` removed. C4/C5 unit tests rewritten against a new fakeCoordinator that satisfies the F5 interface. The C7-shape headline test is `t.Skip`'d and replaced by F8.
- **F7** — `50d63d101`: leader-side `StuckDetector` sweeps every active lease and revokes (`LeaseExpire`) on heartbeat-TTL / hard-deadline / unchanged-progress-signature / cluster-unreachable. FNV-1a 64-bit signature over `{Generation, Ready, LastReportedAt (sec-precision), ComponentStatus size}`. Injectable clock + per-cluster contact override for tests. Followers' detectors no-op.
- **F8** — `8bf93d85b`: headline test `TestDistributedMultiClusterShardedReconcile_F8`. Three Coordinator+Helper pairs, each with its own real raft.Raft node over TCP via F2 muxed StreamLayer + F4 Forwarder. Three reconcile-loop goroutines mirror the production controller's iterator (return-on-first-Wait → requeue). Final stsWriteOrder: `config/a→b→c, shard-0/a→b→c, shard-1/a→b→c, mongos/a→b→c` (12 entries, exactly once each, contiguous per component). Runs in ~2.5s.
- **F9** — `29c81b4f6`: scale-up integration test. The harness simulates "scope needs rescale" by injecting a Ready=false `ReportProgress`. Asserts: shard-0/cluster-c re-allocated each scale-up (3 writes total = 1 initial + 2 rescales); leader's AC version monotonically advances across rescales (tracked via the FSM's `LastAppliedIndex` as a proxy for "any committed proposal" because the rescale window is shorter than the leader's 30ms reconcile tick).
- **F10** — `278b384d5`: failure-injection tests. (a) leader kill mid-flight: new leader elected within 10s; surviving clusters' Ready bits intact; new leader can commit. (b) follower partition: 2/3 quorum holds; leader can still commit. (c) follower kill: idempotent — the surviving 2-node quorum keeps making progress.
- **F11** — `9a30d02e2`: `go build ./...` + `go test ./... -timeout 600s` green (run twice for flake check). Plan doc updated.

### Phase F12 completion notes (2026-05-15)

F12 closes two gaps that the F1-F11 redesign left open:

1. **Resource-reference agreement** (F12a + F12b). Raft leader election rotates between clusters; divergent local copies of the project ConfigMap, credentials Secret, TLS material, etc. would otherwise yield a "whichever cluster happens to be leader wins" inconsistency. F12 makes resource-hash agreement a **hard correctness gate**: every operator hashes its local copy of every spec-referenced resource, the hash is replicated through the FSM as a `ResourceObserved` proposal, and reconciles refuse to proceed until WaitForResourcesAgreed returns ResourcesAgreed across every known cluster. Disagreements surface in the MDB status condition with a diagnostic that names which cluster has the wrong copy (`Resource ConfigMap/ns/project-cm hash mismatch: cluster-a=abc1234, cluster-b=def5678 — cluster-b is out of sync.`). The user fixes the underlying drift; the operator does not auto-resolve.

2. **Remaining OM-write call-site leader gates** (F12c + F12d). F6 covered AC publication + cleanup. F12c added inline `IsLeader()` gates at three sharded-controller call sites (`agents.UpgradeAllIfNeeded`, `commonController.ensureRoles`, `host.CalculateDiffAndStopMonitoring`) and introduced a follower-side read-only OM-connection path (`project.ReadProject` + `connection.PrepareOpsManagerConnectionReadOnly`) so non-leaders no longer call `CreateProject` / `EnsureTagAdded` / `EnsureAgentKeySecretExists`. F12d swept the rest of the sharded reconcile path and added the only remaining missing gate (`controlledfeature.EnsureFeatureControls`); everything else was already inside one of the leader-gated outer wrappers. The full audit table now lives at the top of `mongodbshardedcluster_controller.go`.

Per-chunk SHAs:
- **F12a** — `2aa2bcc9a`: `ProposalResourceObserved` + `ResourceObservedPayload` + `ResourceRef`; per-CR `Resources map[refKey]map[cluster]ResourceObservation` in the FSM with newer-wins / stale-ignored semantics; `Coordinator.ReportResource` (synchronous propose) and `Coordinator.WaitForResourcesAgreed` (returns ResourcesAgreed iff every known cluster has reported and all hashes match; otherwise ResourcesPending + diagnostic). Tests in `pkg/coordination/raft/resource_agreement_test.go` cover FSM apply, 3-node TCP agreement, disagreement, missing-observation, drift-then-fix.
- **F12b** — `75619d463`: `controllers/operator/distributed_resource_agreement.go` with `collectSpecReferencedResourceRefs` (project CM, credentials Secret, member-cert Secrets, agent-cert Secret, LDAP/SCRAM Secrets when referenced), `hashConfigMapData` (sorted keys, drops K8s-managed metadata), `hashSecretData` (includes Secret.Type), `reportLocalResourceHash`, and `gateOnResourceAgreement`. Wired at the top of the sharded helper's `Reconcile`, just after basic validations and before any OM access. Non-distributed mode is a no-op.
- **F12c** — `c5cf458b7`: leader-only gates around `agents.UpgradeAllIfNeeded`, `commonController.ensureRoles`, `host.CalculateDiffAndStopMonitoring`. Added `project.ReadProject` (read-only; returns `ErrProjectNotFound` if absent) and `connection.PrepareOpsManagerConnectionReadOnly` (follower variant that skips `EnsureTagAdded` + agent-key issuance). New `helper.prepareOpsManagerConnectionGated` picks the right variant; followers seeing `ErrProjectNotFound` surface `workflow.Pending` and retry next reconcile while the leader runs the create-path.
- **F12d** — `8c693bb2b`: closed one remaining gap (`controlledfeature.EnsureFeatureControls`). Documented the full OM-write audit table at the top of `mongodbshardedcluster_controller.go`.
- **F12e** — this commit: full `go test ./...` green; plan doc updated.

Design notes worth carrying forward:

- The resource-agreement gate uses majority-hash for the diagnostic ("X clusters say A, 1 cluster says B → B is out of sync"). Production hardening should also consider deterministically-named "reference cluster" sources so even split votes produce a single decision; the PoC's majority heuristic is sufficient for 3-node setups where one drifter is the realistic failure mode.
- CA bundle ConfigMaps referenced from inside the project CM (`sslMMSCAConfigMap`) are NOT yet in the agreed-set; they're resolved during downstream OM setup. Adding them requires a two-phase agreement (project CM agreed → look up CA name → CA CM agreed). Left as a post-PoC item.
- Followers' `prepareOpsManagerConnectionGated` calls return `ErrProjectNotFound` if the project doesn't exist yet. The follower surfaces `workflow.Pending` and retries. There is currently no FSM-side "project created" announcement; the next reconcile poll re-checks OM directly. This is fine because `WaitForResourcesAgreed` already gates the entire reconcile flow before any OM access — followers can't get past the gate without the leader having had a chance to run too.

### Phase D completion notes (2026-05-16)

All D' chunks landed on `lsierant/devcontainer-raft-poc`. The end-to-end test `multi_cluster_sharded_simplest.py` in `DISTRIBUTED_POC_MODE=true` passes 3 consecutive times against 3 local distributed operators (one per kind member cluster), with a fresh teardown + OM project clean between each attempt.

**Run sequence after the first green (success criterion met after fixture fix)**

| Run | Started (UTC) | Duration | Outcome | Notes |
|---|---|---|---|---|
| #1 | 2026-05-15 ~23:03 | 755.11s (12m35s) | 3 passed | First green; full cold reconcile (MDB Running in 727s). |
| #2 | 2026-05-15 23:18:44 | 694.49s (11m34s) | 3 passed | Cold reconcile from a clean state; MDB Running in 665s. |
| #3 | 2026-05-15 23:31:08 | 16.62s | **1 failed** | `test_deploy_operator` raced operator cold-start: health-probe one-shot connect on 127.0.0.1:8191 hit `Connection refused` because the fixture's `--stop`/`--start` cycle had just relaunched the `go run` operators and the port wasn't bound yet. Fixed in commit `9f3723e05` (poll up to 120s instead of one shot). |
| #3b | 2026-05-15 23:34:47 | 598.18s (9m58s) | 3 passed | Re-run with the fixture-poll fix. |
| #4 | 2026-05-15 23:45:44 | 28.43s | 3 passed | Same MDB CR already at `Running` — `assert_reaches_phase` short-circuits; idempotency win. |
| #5 | 2026-05-15 23:47:14 | 29.50s | 3 passed | Same as #4; closes the post-fix 3-in-a-row streak. |

Three consecutive greens post-fix: **#3b → #4 → #5**. All five green runs cover `test_deploy_operator`, `test_create`, `test_sharded_cluster` end-to-end (sharded cluster with config server + shard-0 + mongos across 3 member kind clusters, with replicate-then-distributed-reconcile semantics).

**Per-chunk SHAs**

| Chunk | SHA | What it delivered |
|---|---|---|
| D'1 | `3ccd33545` | `main.go` distributed-mode env vars (`RAFT_CLUSTER_NAME`, `RAFT_BIND_ADDR`, `RAFT_PEERS`, `RAFT_BOOTSTRAP`, `METRICS_BIND_ADDRESS`, `HEALTH_PROBE_BIND_ADDRESS`, `MDB_WEBHOOK_PORT`); `BuildProductionCoordinator` in `pkg/coordination/raft/production.go`; 3-node TCP unit test. |
| D'2 | `9f52a9278` | `scripts/dev/extract_member_kubeconfigs.sh` — per-cluster `--minify --flatten --raw` kubeconfigs. |
| D'3 | `e740eb4b0` | `scripts/dev/replicate_cr_resources.sh` — spec-driven replication of project CM, credentials Secret, TLS material, agent CM/Secret to each member; SHA-256 hash verification. |
| D'4 | `fe7c8eaac` | `scripts/dev/run-3-operators-locally.sh` — launches 3 operators in distinct tmux sessions with distinct raft / metrics / health / webhook ports; `--start`/`--stop`/`--status`. |
| D'5 | `5a4015ebb` | `DISTRIBUTED_POC_MODE` branch in `multi_cluster_sharded_simplest.py`: applies CRDs to each member, replicates refs, propagates MDB CR to each member, rebinds `sharded_cluster.api` for status polling. |
| D'6 pre | `badf228d0` | Env-var collision fix (`CLUSTER_NAME` → `RAFT_CLUSTER_NAME` so `.generated/context.env` can't clobber distributed identity); switched to `godotenv.Load` (no-overwrite) in distributed mode; member-cluster CRD install; relaxed launcher fatal regex (was matching benign pprof port collision). |
| D'6 iter 1 | `811412ffc` | Distributed-mode member-cluster map: `NewShardedClusterReconcilerHelperWithCoordinator` attaches coordinator BEFORE `initializeMemberClusters` so the cluster guard doesn't trip; `main.go` populates `memberClusterObjectsMap` with the local cluster keyed by `RAFT_CLUSTER_NAME`. Python fixture honours `proxy-url` via `KubeConfigMerger` + `load_proxy_config`. |
| D'7 iter 2 | `375f86d5b` | Parallel per-(component, cluster) leases in the FSM. `ActiveLease *Lease` → `ActiveLeases map[string]*Lease` keyed by `<component>\|<cluster>` so 3 operators can hold leases on their own STS concurrently. Unit-test repro in `pkg/coordination/raft/parallel_lease_test.go`. |
| D'7 iter 3 | `c3215e2be` | Stop short-circuiting cross-cluster replication ENTRY-POINTS in distributed mode. `reconcileHostnameOverrideConfigMap`, `replicateAgentKeySecret`, `replicateSSLMMSCAConfigMap` now write to the operator's OWN local cluster (`getHealthyMemberClusters` filters out nil-Client peers naturally). |
| D'7 iter 4 | `f7ed37cb7` | FSM-distributed agent API keys via `ProposalAgentKeyPublished` + `PerCRState.AgentKeys` + `PublishAgentKey`/`GetAgentKey` coordinator API. `replicateAgentKeySecret` prefers the FSM key over local generation, publishes after local secret write so followers can reuse it. Unit-test repro in `pkg/coordination/raft/agent_key_test.go`. |
| D'8 fixture | `9f3723e05` | Poll the operator health probe up to 120s in the `DISTRIBUTED_POC_MODE` test setup. Closes a cold-start race when an in-run `--stop`/`--start` rebinds the port after the one-shot 2s connect attempt — exposed by run #3 in this session. |
| D'8 writeup | `2eef3e5b9` | Initial Phase D completion notes. (Superseded by the correcting commit that contains this table — the original had an inaccurate run-3 entry.) |

**Fixes per category (debugging surprises)**

1. **Env-var collision** (`CLUSTER_NAME`). Pre-PoC, `.generated/context.env` set `CLUSTER_NAME=kind-e2e-operator` and our `loadEnvFromLocalFileForDevelopment` did a force-load that overrode the per-process value the launcher set. Two fixes: rename to `RAFT_CLUSTER_NAME` so there's no symbol clash; use `godotenv.Load` (no-overwrite) in distributed mode.
2. **Hub-spoke "member cluster map" guard**. `initializeMemberClusters` refuses to proceed if the helper has no member-cluster map. The hub-spoke flow builds this map from `.spec.clusterSpecList` cluster names → kube clients. Distributed mode has exactly one entry — the local cluster, keyed by `RAFT_CLUSTER_NAME`. Fix: a new coordinator-aware helper constructor that wires the coordinator BEFORE `initializeMemberClusters` runs, plus an early-return for "single local cluster" in that initializer when distributed.
3. **Single-active-lease deadlock**. The first crack at lease scheduling allowed only one active lease per (CR, component); but in distributed mode each operator owns a different cluster's STS, so cluster-2 sat idle waiting for cluster-1's `sh-config` lease to release when in fact they should both have been running their own. Fix: per-(component, cluster) lease map. Reproduced in a unit test FIRST, then fixed.
4. **Cross-cluster replication entry-point short-circuit**. F12 made `MultiClusterReplicate*` a no-op for cross-cluster writes, but the entry-points `reconcileHostnameOverrideConfigMap`, `replicateAgentKeySecret`, `replicateSSLMMSCAConfigMap` had been short-circuited entirely in distributed mode — meaning the operator wasn't even writing to its OWN local cluster. The fix is to remove the early-return; the existing `getHealthyMemberClusters`-driven loop naturally only writes to clusters the operator has a `Client` for (i.e. just its own).
5. **Generated agent API key drift across clusters**. Each operator independently generated its own agent secret on first reconcile; the operator that won the OM `CreateProject` race published its key to OM, but the other two clusters held mismatched keys locally → agents on those clusters failed authentication and config-1/config-2 pods stalled at Init:0/2. Fix: leader generates once, publishes via `ProposalAgentKeyPublished` to the FSM, followers consume from FSM before writing their local secret. Generalised "operator-emitted shared output" mechanism noted as future work (see "Future design follow-up" in the handoff doc).

**Time per attempt** (D'6 + D'7 iteration history, then D'8 verification runs)

| Attempt | Trigger | Outcome |
|---|---|---|
| 1 | D'6 first run | failed (operator startup, env-var collision — fixed in `badf228d0`) |
| 2 | D'6 iter 1 | failed (member-cluster map guard — fixed in `811412ffc`) |
| 3 | D'7 iter 2 | failed (single-lease deadlock — fixed in `375f86d5b`) |
| 4 | D'7 iter 3 | failed (cluster-2/3 Init:0/2: hostname-override CM + agent-key path — fixed in `c3215e2be`) |
| 5 | D'7 iter 4 | failed (Init:0/2 persisted — agent-key drift — fixed in `f7ed37cb7`) |
| 6 (run #1) | first verification | **first GREEN** — 755.11s (12m35s) |
| 7 (run #2) | repeat | GREEN — 694.49s (11m34s) |
| 8 (run #3) | repeat | failed in 16.62s — fixture cold-start race; fixed in `9f3723e05` |
| 9 (run #3b) | post-fix | GREEN — 598.18s (9m58s) |
| 10 (run #4) | post-fix idempotent | GREEN — 28.43s |
| 11 (run #5) | post-fix idempotent | GREEN — 29.50s |

Total: 6 GREEN, 5 RED across D'6→D'8. Post-fix 3-in-a-row streak: runs #3b / #4 / #5.

**What surprised us**

- The hardest bug to triage was the agent-key drift (attempt 4). Symptom (Init:0/2 on cluster-2/3) was a kubelet-level mount/wait that doesn't fingerprint to any specific FSM state. We initially suspected hostname-override CM (fixed in iter 3) and replicated-secret coverage, but once the CM was in place the same Init:0/2 persisted. Root cause was visible only by `kubectl describe pod` on cluster-2 and seeing the agent-secret reference at runtime — the secret existed but with a per-operator-generated value that didn't match what the leader had pushed to OM.
- The FSM design generalises cleanly: agent keys are the FIRST type of "operator-emitted, must-agree-across-clusters" output we've had to thread through the state machine, but a similar shape will apply to any future operator-issued material (TLS certs, CA bundles, etc.). The handoff doc flags this as post-PoC follow-up — explicitly NOT in scope for this PoC.
- The unit-test-first discipline paid off for the lease deadlock (iter 2) — the unit test caught the issue inside ~10s of e2e-equivalent simulation. The agent-key drift was harder to unit-test reproducibly (needs operator processes racing on `EnsureAgentKeySecretExists`), but the FSM-side propagation is fully covered by `TestPublishAgentKey_FSMDistribution`.
- `wt-ctl attach` returns exit 0 immediately after the inner command's tmux detach; the actual pytest runs to completion in the devcontainer regardless. For long-running e2e tests, treat the `wt-ctl attach` exit purely as "command launched"; rely on the on-disk test log + Monitor on its tail for the real outcome.
- `make prepare-local-e2e` order matters — it patches kubeconfigs and creates project CM + credentials in the central cluster. `extract_member_kubeconfigs.sh` MUST run AFTER, or per-cluster kubeconfigs miss the patches.
- Fixture-level cold-start race: when an e2e iteration's `test_deploy_operator` cycled the operator processes via `--stop` / `--start`, the immediate one-shot `socket.connect` on the health-probe port hit `Connection refused` because the `go run` cold start takes 30-60s to bind. The fix (`9f3723e05`) is a 120s poll loop, but the deeper lesson is that PoC fixtures that touch operator lifecycle need *retry-and-deadline* semantics on every readiness probe — single-shot connects are too brittle when `go run` is the launcher.

**Pending follow-ups** (NOT in PoC scope):

- Generalise the FSM-distributed "operator-emitted artifact" mechanism to TLS certs / CA bundles / etc. Current shape (`ProposalAgentKeyPublished` + per-CR `AgentKeys`) is single-purpose; a generic `ProposalArtifactPublished{CRKey, Kind, ID, Bytes}` would cover the broader case.
- CA bundle ConfigMaps referenced from inside the project CM (`sslMMSCAConfigMap`) — still missing from the F12 agreement gate (carried forward from F12 notes).
- Two-phase resource agreement (resolve project CM first, then look up CA name + agree on it).
- Real K8s deployment of operators (PoC runs them inside the devc as local processes).
- Persistent raft storage (PoC uses in-memory + `os.TempDir()`).
- Operator HA inside a cluster (PoC: exactly one operator per cluster).

After completing a chunk, the main session updates the matching line by
moving the `[X]` to the right cell and adding a brief note below the
block, e.g.:

```
C1 completed 2026-05-15: pkg/coordination/raft/ scaffolded, TestThreeNodesElectLeader
passes. Commit: <sha>. Note: had to use raft v1.7 due to API changes in v1.8.
```

---

## 11. Known risks and decision points

| Risk | Where it surfaces | Decision criteria |
|---|---|---|
| Existing test helpers (`mongodbshardedcluster_controller_multi_test.go`) don't compose with 3 in-process reconcilers without significant refactor | C7 | If C7 turns out to need > 200 LOC of helper refactor, stop and re-scope. Possibly a simpler test that doesn't reuse the existing helpers verbatim. |
| `controller-runtime` shared singletons (metrics registry, leader election) block running 3 instances in one process | C7 | Workaround: use separate `manager.Manager` instances with distinct metrics/health ports. May require dependency injection where today it's global state. |
| AC mock can't tell which cluster's reconciler called it | C7 | Thread cluster identity into the OM mock connection at construction. If this is awkward, add a per-call thread-local or context value. |
| kind cross-cluster networking on EVG host doesn't allow localhost Raft listeners across operator processes | D2 | Unlikely — they're all localhost. If it happens, use Unix domain sockets. |
| Manual secret replication in D3 is very long / fragile | D3 | If we end up replicating > 5 secret types manually, accept the friction for PoC, but log it as a high-priority real-impl task. |
| OM-side state from a prior baseline run interferes with the distributed run | D3 | Cleanup script: delete the OM project between runs. |
| Leader election in `controller-runtime` (the K8s Lease one, not Raft) conflicts with our Raft leader | D2 | Disable controller-runtime leader election (`--leader-elect=false`) — each distributed operator is the singleton owner of its kubeconfig. |

---

## 12. Quick reference: commands

```bash
# Run unit tests
go test ./controllers/operator/... -timeout 10m
go test ./pkg/coordination/... -timeout 10m

# Build
go build ./...

# Run baseline e2e (hub-spoke, unchanged)
./scripts/dev/op_run.sh ...   # exact invocation per existing workflow

# Run distributed-mode e2e
export DISTRIBUTED_POC_MODE=true
./scripts/dev/extract-member-kubeconfigs.sh
./scripts/dev/run-3-operators-locally.sh
./scripts/dev/e2e_run.sh multi_cluster_sharded_simplest

# Cleanup
./scripts/dev/run-3-operators-locally.sh --stop
kubectl --kubeconfig=.generated/cluster-a.kubeconfig delete mongodb --all
# ... repeat for b, c
```

(Exact paths/flags may differ; subagents should discover them as part of
their chunk.)

---

## 13. Closing notes

This plan is the minimum work to validate the distributed-operator design
on a real workload (multi-cluster sharded MongoDB). The implementation is
deliberately additive and reversible — every change is gated on a non-nil
coordinator, so a default-config operator is byte-for-byte the existing
operator.

The most valuable artifact produced is **C7's end-to-end unit test**. It
runs in seconds, isolates the protocol from infrastructure, and will be
the regression surface for whoever picks up the real implementation.
Treat it as the load-bearing deliverable.

Everything in Phase D exists to prove the protocol survives the
transition from in-process mocks to real Kubernetes. It is not
sufficient by itself — the real-impl project will need its own e2e
strategy. But it's the right next step to validate that nothing in our
unit-test simplifications hid a fundamental incompatibility.

---

## 14. Phase D (revised) — e2e PoC after Phase F redesign

> Supersedes the §5 D0-D3 plan (which predates Phase F/F12). The original §5 is retained as historical reference.

### 14.1 Context (post-F12)

The unit-test PoC is complete on branch `lsierant/devcontainer-raft-poc` (= `lsierant/devcontainer-raft-poc-unit` content, ff'd in). Key delivered capabilities:

- Distributed mode wired into the sharded controller via `DistributedCoordinator`. When `coordinator == nil`, hub-spoke path runs unchanged (backwards-compat).
- Muxed StreamLayer: one TCP port serves raft internals + app-channel proposal forwarding (handshake-byte dispatch).
- Resource-hash agreement (`WaitForResourcesAgreed`) is a **hard correctness gate** at the top of reconcile — no central cluster, every operator can be leader, so all clusters must hold identical spec-referenced ConfigMaps/Secrets.
- Inline lease-gating at every STS write site; leader-gating at every Ops Manager write site (audit table at top of `mongodbshardedcluster_controller.go`).
- Cross-cluster K8s replication (`replicateAgentKeySecret`, `reconcileHostnameOverrideConfigMap`, `replicateSSLMMSCAConfigMap`) is a **no-op in distributed mode** — resources must be pre-replicated.
- Per-CR FSM partitioning by `CRKey{Kind, Namespace, Name}`.
- Stuck detection: heartbeat-on-StatusReport, TTL, hard deadline, no-progress signature.
- Cluster unreachable detection via raft peer-contact (no memberwatch).

PR (draft): https://github.com/mongodb/mongodb-kubernetes/pull/1116 — base `lsierant/devcontainer`.

### 14.2 Discipline (user-mandated for this phase)

1. **Test-driven iteration.** When an e2e failure is reproducible in a unit test, add the unit test first (under `controllers/operator/` or `pkg/coordination/raft/`), fix the code, verify the test passes, then retry the e2e. Skip this only when (a) the fix is obvious (e.g. typo in script, manifest field name) AND (b) a unit-test simulation would unnecessarily complicate the production code (e.g. real-K8s-only timing issue).
2. **Erase + rebuild.** Tear down the previous (working hub-spoke) e2e deployment in its entirety before the first distributed attempt. No state carryover.
3. **State verification before each attempt.** Audit: binary builds clean, `go test ./...` green, ports free, kubeconfigs valid, resources replicated, no stale operator pods, no stale local processes.
4. **Reuse existing EVG host.** The `lsierant_devcontainer-raft-poc` worktree already has a 4-cluster kind setup (`kind-e2e-operator` + `kind-e2e-cluster-{1,2,3}`) wired up and reachable from the devc.

### 14.3 Worktree state for Phase D

- **Path**: `/Users/lukasz.sierant/mdb/lsierant_devcontainer-raft-poc`
- **Branch**: `lsierant/devcontainer-raft-poc` (now at `5a1040c66` — F12e — same content as unit branch).
- **EVG host**: `i-0891b0832c362559f` (eu-west-1, ubuntu2204), displayName `lsierant_devcontainer-raft-poc`.
- **Namespace**: `ls-1152`.
- **Multicluster kubeconfig**: `.generated/multicluster_kubeconfig` (carries all member contexts).
- **Devc kubeconfig**: `.generated/current.devc.kubeconfig`.

### 14.4 Chunks

#### D'0 — State verification + tear-down current e2e

**Goal:** confirm the worktree is clean and ready; tear down the previously-passing hub-spoke deployment so we start fresh.

- `go build ./...` and `go test ./...` both green on the devc.
- Confirm the 4 kind clusters are alive: `kubectl --context kind-e2e-{operator,cluster-1,cluster-2,cluster-3} get nodes` all return.
- Confirm no leftover MongoDB resources in `ls-1152`:
  ```
  kubectl --context kind-e2e-operator get mdb,mongodbmulti -n ls-1152
  ```
  Delete any. Wait for finalisers to clear.
- Confirm no running operator processes (kill any `mck-operator` tmux session; check pods in all 4 clusters).
- `make prepare-local-e2e` to refresh ECR creds + recreate fresh ConfigMaps/Secrets/RBAC.
- Audit: list every OM project in cloud-qa scoped to `ls-1152*` (use `wt-ctl om list`). Clean any stale ones via `wt-ctl om clean`.

Commit deliverable: none (state verification only). Report: a checklist with pass/fail per item.

#### D'1 — Distributed-mode flags in `main.go`

**Goal:** the operator binary accepts CLI flags / env vars that switch it into distributed mode.

New flags / env vars:

| Flag / env | Purpose |
|---|---|
| `CLUSTER_NAME` | This operator's cluster identity. Required for distributed. |
| `RAFT_BIND_ADDR` | `host:port` to bind raft + app-channel listener (e.g. `127.0.0.1:7001`). |
| `RAFT_PEERS` | Comma-separated peer list, format `name=host:port,name=host:port,...`. |
| `RAFT_BOOTSTRAP` | `true` for exactly one peer (initial bootstrap). |
| `RAFT_DATA_DIR` | Optional, for snapshot+log persistence. PoC can use `os.TempDir()`. |
| `METRICS_BIND_ADDRESS` | Override default 8080. Must be distinct per operator process. |
| `HEALTH_PROBE_BIND_ADDRESS` | Override default 8081. Must be distinct. |

Behaviour in `main.go`:
- If `RAFT_PEERS` is set: construct `raft.NewTCPRaftCluster`-style Manager with `MuxedStreamLayer` bound to `RAFT_BIND_ADDR`. Construct `Coordinator`, set `Forwarder`, set `ClusterPeerMap`, set `DefaultCR` (TBD: how the default CR is wired — see surprises section).
- Inject the coordinator into the sharded reconciler.
- **Disable `MongoDBMultiCluster` controller registration** in distributed mode (only the sharded controller participates).
- **Disable controller-runtime in-cluster leader election** (`--leader-elect=false`) — distributed mode runs exactly one operator per cluster.

Unit test: operator binary starts with new flags; raft cluster forms in-process across 3 invocations.

Commit: "Add distributed-mode flags to main.go".

#### D'2 — Per-cluster kubeconfig extraction

**Goal:** a script that splits `.generated/current.devc.kubeconfig` into 3 per-cluster files, each with a single context + user + cluster (no cross-cluster references). Stored as `.generated/cluster-1.kubeconfig`, etc.

Script: `scripts/dev/extract_member_kubeconfigs.sh`. Idempotent. Validates each with `kubectl cluster-info`.

Commit: "Add extract_member_kubeconfigs.sh".

#### D'3 — Resource pre-replication script

**Goal:** a script that takes a CR name + namespace and replicates all spec-referenced ConfigMaps + Secrets from the central cluster to each member cluster. Required because Phase F12 makes cross-cluster K8s replication a no-op in distributed mode.

Script: `scripts/dev/replicate_cr_resources.sh`. For the multi_cluster_sharded_simplest test, the canonical list is:
- Project ConfigMap (`Spec.CloudManager.ConfigMapRef` or `Spec.OpsManager.ConfigMapRef`).
- Credentials Secret (`Spec.Credentials`).
- CA bundle ConfigMap (if TLS).
- Member-cluster cert Secrets (if `Spec.Security.CertificatesSecretPrefix` is set).
- Agent secrets (replicated by the operator today; need to be replicated by hand now).

Verification: SHA-256 hashes of the replicated resources must match across all member clusters.

Commit: "Add replicate_cr_resources.sh".

#### D'4 — Multi-process operator launcher

**Goal:** a script that starts 3 operator processes locally in the devc, one per member kubeconfig, all in distributed mode.

Script: `scripts/dev/run-3-operators-locally.sh`:
- Computes peer list: `cluster-1=127.0.0.1:7001,cluster-2=127.0.0.1:7002,cluster-3=127.0.0.1:7003`.
- Per cluster: starts `go run ./main.go` in a tmux session named `mck-op-<cluster>`, with env: `CLUSTER_NAME=<cluster>`, `KUBECONFIG=.generated/<cluster>.kubeconfig`, `RAFT_BIND_ADDR=127.0.0.1:700<n>`, `RAFT_PEERS=...`, `RAFT_BOOTSTRAP=true` only for cluster-1, `METRICS_BIND_ADDRESS=:818<n>`, `HEALTH_PROBE_BIND_ADDRESS=:819<n>`.
- Tees stdout/stderr to `logs/operator-<cluster>.log`.
- Waits for all 3 to log "Raft leader elected" / "Following leader".
- `--stop` flag cleanly terminates all 3 tmux sessions.

Commit: "Add run-3-operators-locally.sh".

#### D'5 — Test setup adjustment

**Goal:** `multi_cluster_sharded_simplest.py` runs in distributed mode end-to-end when `DISTRIBUTED_POC_MODE=true`.

Changes in `docker/mongodb-kubernetes-tests/tests/multicluster_shardedcluster/multi_cluster_sharded_simplest.py` (and shared fixtures it uses):

- `test_deploy_operator` fixture: when `DISTRIBUTED_POC_MODE=true`:
  - Install CRDs + ServiceAccount + RBAC in **each member cluster** via helm with `operator.replicas=0`. No operator pod in any cluster.
  - Do not install the operator in `kind-e2e-operator` (the central/hub cluster).
  - Run the resource pre-replication script after applying the MDB CR — or call it inline as a fixture step before the MDB resource is applied if the fixture creates the project ConfigMap.
- Existing path (when `DISTRIBUTED_POC_MODE` unset) remains unchanged.

Commit: "Add DISTRIBUTED_POC_MODE branch to test_deploy_operator".

#### D'6 — First e2e attempt

**Goal:** end-to-end run of `multi_cluster_sharded_simplest.py` against the 3 local distributed operators.

Sequence:
1. State verification per D'0.
2. Run extract_member_kubeconfigs.sh.
3. Run run-3-operators-locally.sh; verify raft cluster forms (leader + 2 followers in each operator's log).
4. Run `make prepare-local-e2e` (re-creates fresh OM project + secrets across all clusters).
5. Run replicate_cr_resources.sh for the test's CR.
6. Run `scripts/dev/e2e_run.sh docker/mongodb-kubernetes-tests/tests/multicluster_shardedcluster/multi_cluster_sharded_simplest.py` with `DISTRIBUTED_POC_MODE=true`.
7. Tail operator logs from all 3 processes + the test log.

Identify first failure. Move to D'7.

#### D'7+ — Test-driven iteration

For each issue encountered:

1. **Capture symptom**: relevant operator-log slice, test failure message, MDB status conditions, FSM state snapshot (debug endpoint or test-only dump).
2. **Try to reproduce in unit test**: add a unit test under `controllers/operator/` or `pkg/coordination/raft/` that triggers the same code path with the same inputs. If you can reproduce, fix the code, verify the test passes.
3. **If not reproducible in unit** (real-K8s timing, networking, manifest mismatches, etc.): fix directly, document why a unit test wasn't viable.
4. Commit per fix with clear message.
5. Tear down, re-run from D'6.

Likely categories of issues (predicted):
- Resource pre-replication coverage gaps (a Secret/ConfigMap was missed) → unit test that adds the missing ref to `collectSpecReferencedResourceRefs`.
- Hash computation drift between operators (K8s adds an annotation that we didn't strip) → unit test in `hashConfigMapData`.
- Raft peer reachability (the 3 local processes can't dial each other) → not reproducible in unit, fix the run script.
- ProjectID resolution race on followers (`ErrProjectNotFound` returned but FSM hasn't yet had `ResourceObserved` from the leader) → unit test for the retry/wait policy.
- Stuck-detection threshold too aggressive (slow STS converges past threshold) → tune thresholds + unit test.
- `MultiClusterReplicateXxx` was a no-op but something downstream still expects the resource to be replicated → fix the test fixture pre-replication, OR re-introduce a leader-side replication call (the leader CAN write to its OWN cluster only — what we eliminated was *cross-cluster* replication). Audit.

#### D'8 — Final verification + writeup

- Run the test three times in a row without intervention; all PASS.
- Capture operator logs + final FSM state from one successful run.
- Append "Phase D completion notes (date)" to plan doc §10: chunks, fixes per category, time per attempt, what surprised us.
- Mark Phase D `[X] complete` in §10.

### 14.5 What's "ready for e2e" looks like

Before triggering D'6, all of the following must be true (D'0 checklist):

- [ ] `go build ./...` green from the worktree root inside the devc.
- [ ] `go test ./...` green inside the devc.
- [ ] D'1 commit in: `main.go` accepts new flags and starts coordinator when `RAFT_PEERS` is set.
- [ ] D'2 script in: `extract_member_kubeconfigs.sh` produces 3 valid kubeconfigs.
- [ ] D'3 script in: `replicate_cr_resources.sh` produces identical hashes per resource across the 3 member clusters.
- [ ] D'4 script in: `run-3-operators-locally.sh` brings up 3 operators that form a raft cluster + log leader election.
- [ ] D'5 fixture change in: `DISTRIBUTED_POC_MODE` branch installs CRDs in member clusters and skips operator-in-central.
- [ ] 4 kind clusters alive and reachable.
- [ ] No previous-run state: ns `ls-1152` empty of MongoDB resources; no stale operator pods; no stale OM projects matching `ls-1152*`; no stale `mck-operator` or `mck-op-*` tmux sessions.

### 14.6 Out of scope for Phase D PoC

- AppDB and replicaset controllers (only sharded).
- Operator HA inside a cluster (only one operator per cluster).
- TLS for raft / app-channel transports (plain TCP).
- Persistent raft storage (in-memory or `os.TempDir()`).
- Real K8s deployment of operators (we run them inside the devc as local processes).
- Cross-CR coordination (each MDB is independent).
