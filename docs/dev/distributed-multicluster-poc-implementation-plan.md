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
Phase A — Environment setup:        [ ] not started   [ ] in progress   [ ] complete
Phase B — Baseline validation:      [ ] not started   [ ] in progress   [ ] complete
  B1 — Baseline e2e run:            [ ] not started   [ ] in progress   [ ] complete
Phase C — Unit-test PoC:            [ ] not started   [ ] in progress   [ ] complete
  C0 — Survey test scaffolding:     [ ] not started   [ ] in progress   [ ] complete
  C0b — Survey AC/STS gate sites:   [ ] not started   [ ] in progress   [ ] complete
  C1 — Raft pkg scaffold:           [ ] not started   [ ] in progress   [ ] complete
  C2 — FSM + proposals:             [ ] not started   [ ] in progress   [ ] complete
  C3 — Coordinator interface:       [ ] not started   [ ] in progress   [ ] complete
  C4 — Gate AC sites:               [ ] not started   [ ] in progress   [ ] complete
  C5 — Lease-gate STS sites:        [ ] not started   [ ] in progress   [ ] complete
  C6 — Leader scheduler:            [ ] not started   [ ] in progress   [ ] complete
  C7 — E2E unit test:               [ ] not started   [ ] in progress   [ ] complete
  C8 — Regression check:            [ ] not started   [ ] in progress   [ ] complete
Phase D — e2e PoC:                  [ ] not started   [ ] in progress   [ ] complete
  D0 — Extract kubeconfigs:         [ ] not started   [ ] in progress   [ ] complete
  D1 — Modify test_deploy_operator: [ ] not started   [ ] in progress   [ ] complete
  D2 — Run 3 operators locally:     [ ] not started   [ ] in progress   [ ] complete
  D3 — Run e2e test:                [ ] not started   [ ] in progress   [ ] complete
Phase E — Verification:             [ ] not started   [ ] in progress   [ ] complete
  E1 — Repeat run:                  [ ] not started   [ ] in progress   [ ] complete
  E2 — DR drill (stretch):          [ ] not started   [ ] in progress   [ ] complete
  E3 — Findings writeup:            [ ] not started   [ ] in progress   [ ] complete
```

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
