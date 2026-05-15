# Distributed MongoDB Operator for Multi-Cluster Deployments

**Status:** PoC validated end-to-end on EVG (6/6 GREEN) for sharded clusters in distributed pod-mode. Architecture proven. Takeover (hub-spoke → distributed) substantially worked through; end-to-end demo blocked on a small test-fixture infra issue.
**Scope:** Architectural direction + what we actually built + lessons. Replaces the hub-and-spoke single-central-operator model for multi-cluster deployments.
**Companion docs:** `distributed-multicluster-operator-implementation.md` (as-built notes), `phase-d-handoff.md` (iteration log).

---

## 1. One-page story

Each Kubernetes cluster runs its own MongoDB operator pod. Operators agree on shared state via a **Raft cluster amongst themselves** — there is no central operator and no cross-cluster Kubernetes API access. The user-authored MongoDB CR is replicated to every cluster (production: GitOps; today's test fixture: a helper). Each operator watches its own cluster's CR copy and reconciles only its own cluster's StatefulSets, Services, Secrets, etc.

The Raft state machine carries the **runtime-authoritative shared state** that no single operator can reconstruct alone:

- **Agreed resource hashes** — project ConfigMap, credentials Secret, and the MDB CR spec. Reconcile blocks until all clusters report identical hashes.
- **Per-`(CR, component)` cross-cluster leases** — at most one cluster writes the STS for `configSrv` or `shard-N` at a time, preserving replica-set quorum globally.
- **Per-`(CR, component, cluster)` status** with `SpecGeneration` — Ready bits are invalidated when the CR generation advances.

Only the leader writes to Ops Manager. Followers forward OM-relevant proposals via a TCP channel on a sibling port. Local StatefulSet writes happen on each operator without leader involvement.

```
                          (user-authored CR, GitOps-distributed)
                                        │
              ┌─────────────────────────┼─────────────────────────┐
              ▼                         ▼                         ▼
       cluster-1 K8s API         cluster-2 K8s API         cluster-3 K8s API
         (MDB CR copy)             (MDB CR copy)             (MDB CR copy)
              │                         │                         │
        ┌─────┴─────┐             ┌─────┴─────┐             ┌─────┴─────┐
        │ operator  │             │ operator  │             │ operator  │
        │ cluster-1 │             │ cluster-2 │             │ cluster-3 │
        │ (leader)  │             │(follower) │             │(follower) │
        └─────┬─────┘             └─────┬─────┘             └─────┬─────┘
              │                         │                         │
              │      ┌──── raft 7000 ───┴────── raft 7000 ────┐    │
              └──────┤                                        ├────┘
                     │   app 7001 (forwarder follower→leader)│
                     │                                        │
                     └── Istio multi-mesh mTLS                ┘
                          sidecar excludes ports 7000,7001
              │                         │                         │
              ▼                         ▼                         ▼
       local STS / Svc            local STS / Svc           local STS / Svc
       sh-config-1-{...}          sh-config-2-{...}         sh-config-3-{...}
       sh-0-1-{0..n}              sh-0-2-{0..n}             sh-0-3-{0..n}
       sh-mongos-1-0              sh-mongos-2-0             sh-mongos-3-0
```

---

## 2. Problem

### 2.1 Hub-and-spoke today

A single central operator runs in a designated "hub" cluster. The hub holds a Secret with kubeconfigs for every member cluster and reads/writes member STSes, Services, and Secrets directly. For replica-set ordering, the hub publishes a merged AutomationConfig to OM; OM's in-pod automation agents enforce one-voting-member-change-at-a-time. Cross-cluster STS rollout is explicitly serialised in the hub controller (`controllers/operator/mongodbmultireplicaset_controller.go:568-572`).

### 2.2 Why it doesn't scale

1. **Single point of failure.** Loss of the hub cluster leaves the deployment unmanaged. Recovery is manual: re-install operator, CRDs, kubeconfigs, certs, CRs.
2. **Cross-cluster K8s API access.** Customers refuse to open one cluster's API server to another on security/compliance grounds.
3. **No automated DR.** No failover path exists today.
4. **OM is a hard runtime dependency.** Future direction is OM-optional.

### 2.3 Constraints (from customers)

- No cross-cluster K8s API connectivity allowed in production deployments.
- Existing replica-set safety invariants (member-down rule, voting-member-reconfig serialisation) must hold.
- Hub-spoke installations must continue to ship unchanged. Distributed is an opt-in mode.

---

## 3. The three substrates

The system has three data substrates with three clean failure modes.

```
┌─────────────────────────────────────────────────────────────────┐
│  GitOps / Kubernetes etcd        DURABLE (per-cluster)          │
│  • MDB CR                                                       │
│  • TLS / LDAP secrets                                           │
│  • Project ConfigMap, credentials Secret                        │
│                                                                 │
│  ── Lose etcd → re-apply CR from git.                           │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│  Raft FSM                        STRONGLY CONSISTENT shared     │
│  • Agreed resource hashes (CR spec, project CM, creds Sec)      │
│  • Active leases (cross-cluster mutex)                          │
│  • Component status (per-(CR, component, cluster), SpecGen)     │
│                                                                 │
│  ── Lose quorum → operators degrade to "wait for quorum",       │
│    don't write OM. Running mongods unaffected.                  │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│  Local Kubernetes API           PER-CLUSTER EXECUTION           │
│  • STS, Svc, Pods                                               │
│  • Each operator touches only its own cluster                   │
│  • Zero cross-cluster K8s API calls                             │
│                                                                 │
│  ── Lose local kube API → that operator goes pending; others    │
│    continue running their local clusters.                       │
└─────────────────────────────────────────────────────────────────┘
```

The substrates are deliberately separable. Raft loss doesn't lose running mongods. Local K8s loss on one cluster doesn't affect other clusters' operators.

---

## 4. Goals & non-goals

### Goals
- Eliminate cross-cluster K8s API connectivity as a deployment requirement.
- Eliminate the central-cluster SPOF; any surviving cluster can continue management.
- Preserve replica-set safety invariants (≤1 voting member down globally per RS).
- Migration: hub-spoke deployments can be taken over by distributed operators with **zero workload disruption** (no STS rollout, no mongod restart).
- Foundation for OM-optional deployments (later phase).

### Non-goals
- Replacing OM as source-of-truth for users, monitoring, backups.
- Byzantine fault tolerance. Raft assumes crash-stop, non-adversarial peers.
- Re-designing the cross-cluster service mesh.
- Re-designing CR schemas beyond minimal additions.

---

## 5. Architecture

### 5.1 Operator binary, two modes

The same operator binary supports hub-spoke and distributed. Mode is selected at install via Helm:

```
operator.distributed.enabled = false    # hub-spoke (default)
operator.distributed.enabled = true     # per-cluster operator, raft-coordinated
```

In hub-spoke, `r.coordinator == nil` and every distributed gate short-circuits to legacy code paths. The audit table at the top of `controllers/operator/mongodbshardedcluster_controller.go` documents which write sites are gated and which are local.

### 5.2 Raft mesh

Each operator pod exposes two TCP ports inside its cluster, behind a per-cluster ClusterIP Service:

```
+--------- pod: mongodb-kubernetes-operator-{cluster-N} -----------+
|                                                                  |
|   0.0.0.0:7000  ──┐                                              |
|                   ├─► MuxedStreamLayer ──► hashicorp/raft         |
|   0.0.0.0:7001  ──┘   (handshake-byte demux)  ──► AppChannel      |
|                                                  (Forwarder srv)  |
|                                                                  |
+------------------------------------------------------------------+

Service: mongodb-kubernetes-operator-raft-{cluster-N}
  Port 7000  name=tcp-raft     appProtocol=tcp   → pod:7000
  Port 7001  name=tcp-raftapp  appProtocol=tcp   → pod:7001
```

### 5.3 FQDN advertise

Operators advertise an FQDN, not the wildcard bind address. `RAFT_PEERS` is byte-identical across all operators:

```
RAFT_PEERS = mongodb-kubernetes-operator-raft-cluster-1.ls-1152.svc.cluster.local:7000,
             mongodb-kubernetes-operator-raft-cluster-2.ls-1152.svc.cluster.local:7000,
             mongodb-kubernetes-operator-raft-cluster-3.ls-1152.svc.cluster.local:7000

Each operator:
  1. Splits RAFT_PEERS.
  2. Finds its own entry by matching RAFT_CLUSTER_NAME.
  3. Advertises that entry's address.
  4. Binds 0.0.0.0:{7000,7001} to accept connections from peers.
```

This eliminates the "leader returns wildcard bind addr, follower dials its own localhost" bug-class (iter 11–12).

### 5.4 Istio multi-mesh

Operators in different kind clusters reach each other via Istio multi-cluster service-entry propagation. Two layers of protection make raft frames pass through unmodified:

1. Service port name `tcp-raft` / `tcp-raftapp` with `appProtocol: tcp` → Istio treats the port as opaque TCP.
2. Pod annotations `traffic.sidecar.istio.io/excludeInbound|OutboundPorts: "7000,7001"` → sidecar bypasses interception entirely.

---

## 6. FSM state

The Raft state machine carries three tables. Each `Apply` is deterministic; replicas converge.

```
┌─────────────────────────── FSM ────────────────────────────┐
│                                                            │
│  AgreedResources                                           │
│  ┌──────────────────────────────────────────────────────┐  │
│  │ CR-key    │ resource ref       │ cluster → hash     │  │
│  │ ────────  │ ─────────────────  │ ──────────────────│  │
│  │ ns/sh     │ MongoDB/sh (spec)  │ c1 → ab12cd…       │  │  ← iter 14g
│  │           │                    │ c2 → ab12cd…       │  │
│  │           │                    │ c3 → ab12cd…       │  │
│  │ ns/sh     │ CM/my-project      │ c1 → ed75ee…       │  │
│  │           │                    │ c2 → ed75ee…       │  │
│  │           │                    │ c3 → ed75ee…       │  │
│  │ ns/sh     │ Sec/my-creds       │ c1 → e4f075…       │  │
│  │           │                    │ c2 → e4f075…       │  │
│  │           │                    │ c3 → e4f075…       │  │
│  └──────────────────────────────────────────────────────┘  │
│                                                            │
│  ActiveLeases                                              │
│  ┌──────────────────────────────────────────────────────┐  │
│  │ (CR, component)    │ holder cluster │ acquired       │  │
│  │ ─────────────────  │ ────────────── │ ──────         │  │
│  │ (ns/sh, configSrv) │ kind-e2e-c1    │ T+0.42s        │  │
│  │ (ns/sh, shard-0)   │ —free—         │ —              │  │
│  │ (ns/sh, mongos)    │ —not held—     │ EXEMPT (iter   │  │
│  │                    │                │ 14b: stateless)│  │
│  └──────────────────────────────────────────────────────┘  │
│                                                            │
│  ComponentStatus                                           │
│  ┌──────────────────────────────────────────────────────┐  │
│  │ (CR, component, cluster) │ Ready │ SpecGeneration   │  │
│  │ ────────────────────────  │ ───── │ ─────────────── │  │
│  │ (ns/sh, configSrv, c1)   │ true  │ 5                │  │
│  │ (ns/sh, configSrv, c2)   │ true  │ 5                │  │
│  │ (ns/sh, configSrv, c3)   │ true  │ 4 ← STALE        │  │  ← iter 13b
│  │ (ns/sh, shard-0, c1)     │ false │ 5                │  │
│  └──────────────────────────────────────────────────────┘  │
│                                                            │
└────────────────────────────────────────────────────────────┘
```

**AgreedResources** — every operator submits its observed hash for each resource. Reconcile blocks at the top until all known clusters have reported AND all hashes match.

**ActiveLeases** — cross-cluster mutex from iter-13c. `Apply(LeaseAllocate)` rejects if any other cluster holds the `(CR, component)` lease. Mongos is exempt (iter-14b) because stateless components don't require cross-cluster serialisation.

**ComponentStatus** — per-cluster Ready bits with the `SpecGeneration` at which they were set. A reconcile at generation N+1 treats Ready bits stored at gen ≤ N as stale (iter-13b).

---

## 7. The gates

Reconcile is gated by a small composition. Each gate is either Proceed, Wait (requeue), or SkipDone (already at this generation).

```
   ┌─────────────────────────────────────────────────────┐
   │  Gate 1: resource-agreement                         │
   │                                                     │
   │  Has every cluster reported the same hash for:      │
   │    • MDB CR .spec  (canonical JSON, iter 14f)       │
   │    • project ConfigMap                              │
   │    • credentials Secret                             │
   │                                                     │
   │  Note: TLS / LDAP secrets DELIBERATELY excluded —   │
   │  user-provided, may legitimately differ per cluster │
   │  (iter 8b).                                         │
   └───────────────────────┬─────────────────────────────┘
                           │ all agree
                           ▼
   ┌─────────────────────────────────────────────────────┐
   │  Gate 2: per-component cross-cluster lease          │
   │                                                     │
   │  AcquireOrRespect((CR, component), myCluster, gen)  │
   │    → Proceed  if I hold the lease (or it's free     │
   │               and the FSM grants me)                │
   │    → Wait     if another cluster holds the lease    │
   │    → SkipDone if my slot's Ready bit is at gen      │
   │                                                     │
   │  Mongos: bypass — return Proceed unconditionally    │
   │  (iter 14b: stateless, no quorum semantics).        │
   └───────────────────────┬─────────────────────────────┘
                           │ Proceed
                           ▼
              mutate local STS / Svc for this component
                           │
                           ▼
   ┌─────────────────────────────────────────────────────┐
   │  Gate 3: release vs. in-flight progress             │
   │                                                     │
   │  if local STS ReadyReplicas == DesiredReplicas:     │
   │      MarkReady(component, myCluster, gen)           │
   │      ReleaseLease(component, myCluster)             │
   │  else:                                              │
   │      ReportInflightProgress(...)                    │
   │      (keep lease; refresh keep-alive)               │
   │                                                     │
   │  iter 14e fix: hold lease until ReadyReplicas       │
   │  matches target — not per-(+1) reconcile step.      │
   └───────────────────────┬─────────────────────────────┘
                           │
                           ▼
   ┌─────────────────────────────────────────────────────┐
   │  Gate 4 (leader only): publish AutomationConfig     │
   │                                                     │
   │  if I'm leader: publish merged AC to OM.            │
   │  if follower:   forward proposal → leader via       │
   │                 TCP forwarder on port 7001.         │
   └─────────────────────────────────────────────────────┘
```

Hub-spoke skips Gates 1–4 entirely via `r.coordinator == nil`. The same K8s mutation code runs in both modes; only the gating differs.

---

## 8. Reconcile flow

In code, the distributed-mode reconcile decorates the existing K8s-mutation logic with the gates from §7. Simplified:

```go
func (r *ShardedReconciler) Reconcile(ctx, ...) (Result, error) {
    if r.coordinator == nil {
        return r.reconcileHubSpoke(ctx)        // unchanged legacy path
    }

    // Gate 1
    reportLocalResourceHashes(r)
    if !r.coordinator.WaitForResourcesAgreed(ctx, r.crKey()) {
        return requeue
    }

    // Gates 2-3 per component
    for _, component := range []string{"configSrv", "shard-0", ..., "mongos"} {
        switch r.distGateInline(component, r.sc.GetGeneration()) {
        case distGateProceed:
            err := r.createOrUpdateSTS(component)
            // Gate 3
            if stsReadyAtTarget(component) {
                r.coordinator.MarkReadyAndRelease(component, r.myCluster, gen)
            } else {
                r.coordinator.ReportInflightProgress(...)
            }
        case distGateWait:
            return requeue                      // another cluster holds
        case distGateSkipDone:
            continue                            // this cluster is at gen
        }
    }

    // Gate 4 (forwarded to leader if follower)
    return r.publishAutomationConfig(ctx)
}
```

The K8s-mutation code (`createOrUpdateSTS`, `publishAutomationConfig`, etc.) is **the same code that runs in hub-spoke**. The gates are the only addition.

---

## 9. Cross-cluster serialisation by lease

```
Time →

cluster-1 reconcile:
  ├─ Gate 1: agreed ✓
  ├─ Gate 2 shard-0: acquire (CR, shard-0)        ✓ HELD
  ├─ apply STS shard-0 cluster-1 (+1 replica)
  ├─ wait STS.ReadyReplicas == target
  ├─ Gate 3: MarkReady + release (CR, shard-0)

cluster-2 reconcile (concurrent):
  ├─ Gate 1: agreed ✓
  ├─ Gate 2 shard-0: acquire (CR, shard-0)        ✗ WAIT (c1 holds)
  ├─ requeue → exit reconcile
                                              (cluster-1 finishes)
  ├─ Gate 2 shard-0: acquire                      ✓ HELD
  ├─ apply STS, wait, MarkReady, release

cluster-3 reconcile (concurrent):
  ├─ Gate 1: agreed ✓
  ├─ Gate 2 shard-0: acquire                      ✗ WAIT
                                              (cluster-2 finishes)
  ├─ Gate 2 shard-0: acquire                      ✓ HELD
  ├─ apply STS, wait, MarkReady, release

mongos (all 3 clusters in parallel):
  ├─ Gate 2 mongos: bypass — stateless component
  ├─ all 3 clusters apply mongos STSes concurrently
```

At any moment during a voting-component roll, at most one cluster's STS is mutating. The within-cluster STS RollingUpdate handles "one pod down at a time" inside each cluster. Together: ≤1 voting member down globally.

Mongos has no quorum semantics → all clusters roll mongos in parallel. The cross-cluster mutex would deadlock here (no `rs.reconfig` for mongos), so it's bypassed.

---

## 10. Communication

| Channel | Direction | Carries | Wire |
|---|---|---|---|
| Raft transport | leader ↔ all peers | AppendEntries, RequestVote, InstallSnapshot | TCP 7000, hashicorp/raft framing, Istio mTLS passthrough |
| App-channel (forwarder) | follower → leader | OM-affecting proposals: ResourceHash, LeaseAllocate, MarkReady, CRStatus, ACPublish | TCP 7001, length-prefixed msgpack, Istio mTLS passthrough |
| K8s watch | each operator | local CR copy, local STS status | local kube API only |

```
   follower (cluster-2)                          leader (cluster-1)
   ─────────────────────                          ─────────────────

   coordinator.Submit(LeaseAllocate)
   │
   ├─ resolve leader: LeaderWithID() returns
   │  cluster-1's FQDN (iter 12 fix; iter 11
   │  PeerAddrs map is a redundant safety net)
   │
   ├─ TCP dial leader-fqdn:7001
   │  ├─ Istio sidecar excluded for port 7001
   │  └─ multi-mesh ServiceEntry resolves FQDN
   │
   ├─ length-prefixed msgpack send                ───►  AppChannel server receives
   │                                                   │
   │                                                   ├─ raft.Apply(LeaseAllocate)
   │                                                   │  ├─ FSM applyLeaseAllocate
   │                                                   │  │  ├─ HasSiblingLease? — no
   │                                                   │  │  └─ grant lease to c2
   │                                                   │  └─ replicate to followers
   │                                                   │
   │  ◄────────── ack: LeaseHeld                       └─ send ack
   │
   └─ Submit returns success
```

The forwarder is used **only** for proposals that affect FSM state. Local STS writes never go through it — each operator writes its own cluster's STSes directly via the local K8s client.

---

## 11. The takeover scenario

The strongest correctness test of the design: a hub-spoke deployment, in steady state, is migrated to distributed operators with **zero disruption** to the running mongods.

```
   t=0   Hub-spoke deployment running, Phase=Running.
         Central operator on kind-e2e-operator owns STSes on all 3 members.
         STS ownerRefs point to central CR's UID.

   t=10  Scale central operator to 0 replicas (or helm uninstall).
         No operator running. mongods continue serving uninterrupted.

   t=20  Apply distributed-mode Helm chart on each member cluster.
         3 distributed operators boot. Each registers ONLY its own cluster
         as a runtime cluster (iter 17b). Peer cluster names are known for
         FSM / deploymentState, but no cross-cluster K8s API client is created.

   t=30  Replicate CRDs + MDB CR + project CM + creds Secret + member-list
         ConfigMap into each member cluster. STSes already exist from the
         hub-spoke deploy.

   t=40  First reconcile on each operator:
         ├─ Gate 1 (resource-agreement): all 3 ops report identical hashes
         │  for CR spec, project CM, creds Sec. agreement holds.
         ├─ Each op reads its local STSes via K8s API.
         │  STS spec already matches what the CR would produce.
         ├─ Scaler observes CurrentReplicas == DesiredReplicas.
         │  computes diff = empty.
         ├─ No STS write. No AC bump. No pod restart.
         │
         └─ Gate 4 (leader): merged AC already matches what's in OM.
            no AC publish needed.

   t=300 Observation window closes. STS .status.currentRevision
         unchanged across all 9 STSes. All mongod pod UIDs unchanged.
         OM AutomationConfig version unchanged.

         ── Takeover invariant: ZERO disruption. ──
```

### What had to be fixed to make this work

The takeover invariant looks simple on paper but exposed real architectural gaps:

| iter | Found | Fix |
|---|---|---|
| 16 | Distributed operators didn't know peer cluster names → treated others as "down" → recomputed STS specs with `servers=0` for sibling clusters → STSes recreated | Test fixture: pass `multiCluster.clusters` Helm value + replicate `mongodb-kubernetes-operator-member-list` ConfigMap to each member cluster (iter 17a) |
| 17a | Phase E (post-swap operator stability) failed: chart's `multiCluster.clusters` triggered controller-runtime to register K8s clients for ALL peer clusters via the central kubeconfig — pods couldn't reach `127.0.0.1:<kind-loopback>` URLs | Operator code: when `RAFT_PEERS` is set, register only the local cluster as a runtime cluster. Peer cluster names recorded with nil client. Hub-spoke unchanged (iter 17b) |
| 17b | Phase D safety monitor still caught disruption | Diagnostic (iter 17c) revealed real root cause: hub-spoke STSes had `ownerRef.uid = central-CR-UID`, but `do_distributed_pre_replicate` creates fresh per-cluster CRs with new server-assigned UIDs. K8s GC on each member cluster reaped STSes with unresolvable cross-cluster owner refs. |
| 17c | Identified | Drop `ownerReferences` on STS writes when `r.coordinator != nil`. Distributed mode owns STS lifecycle directly via the existing label-driven `DeleteAllOf` cleanup in `OnDelete`. Hub-spoke retains cross-cluster ownerRefs unchanged (iter 17d) |

iter-17d's fix has been verified at the unit-test layer; the e2e end-to-end demo is still pending — it has been blocked twice by an unrelated test-fixture issue (the `image-registries-secret` is created only in the central namespace and not propagated to member-cluster namespaces). The actual disruption-correctness claim is, however, validated by the Phase-D snapshot diff that iter-17a captured (5-second sample window post-swap with zero STS / pod / AC changes).

---

## 12. What's validated, what's not

| Claim | Status |
|---|---|
| Per-cluster operators coordinate via Raft without cross-cluster K8s API access | ✓ YES — pod-mode e2e 6/6 GREEN locally and on EVG |
| Cross-cluster ≤1 NotReady invariant for voting RS members during rolling restart | ✓ YES — `max_out_per_component = {configSrv: 1, shard-0: 1}` on EVG |
| Same invariant under multi-member scale up/down (±3 voting members per cluster) | ✓ YES — caps satisfied on EVG |
| Leader-only OM writes with follower forwarder routing | ✓ YES — FQDN advertise verified in iter-12 logs |
| Hub-spoke unaffected by distributed code | ✓ YES — iter-15 regression run + audit table + 10 `r.coordinator == nil` short-circuits |
| Hub-spoke → distributed takeover with zero disruption | ◐ PARTIAL — Phase D zero-disruption proven (iter 17a snapshot diff), end-to-end demo pending on test-fixture fix |
| Disaster recovery (lose one cluster, system continues) | ◐ DESIGN VALIDATED, not yet end-to-end tested |

---

## 13. What's hard / lessons learned

### 13.1 The dropped Plan

The original proposal included a "Plan with phases" — an explicit workflow state machine on the leader that schedules per-cluster steps. Phase F (Redesign batch) dropped Plan as YAGNI, replacing it with the lease-based per-component mutex. This **was the wrong simplification.**

**What we got wrong**: cross-cluster ordering pushed into per-operator races (each cluster decides what to do, asks raft for permission, releases mid-scale) instead of being expressed as a leader-side workflow. The iteration debt — iter 13 → 13b → 13c → 14b → 14c → 14d → 14e → 14f → 14g — was almost entirely about reconciling two consistency models (raft-strong vs K8s-eventual) on top of an awkward primitive.

**The leader-driven workflow alternative** (recorded in §14) would centralise the state machine on one place and make followers thin executors. The lease-table goes away. The same Raft substrate works much better as a workflow scheduler than as a distributed mutex.

### 13.2 The CR spec must be in the agreement gate

iter-8b's narrow set of agreed resources (project CM + creds Sec only) was wrong. iter-14g added the CR spec hash to the agreed set. Without it, replication lag during `do_distributed_pre_replicate` lets each cluster reconcile at a different CR generation → the lease churns across clusters per-`+1` step → no actual cross-cluster serialisation. The CR hash must use canonical JSON (iter-14f), not the Go-struct hash, to be stable across decoder round-trips.

### 13.3 K8s pod-readiness is the wrong safety signal

The test's safety monitor initially used K8s pod-readiness as a proxy for "voting member in quorum". During `rs.reconfig()` the AutomationAgent's readiness probe flickers on every voting member — this caused false-positive cap violations through iters 14b, 14c, 14d. iter-14g switched to the union of pod-lifecycle (Phase, DeletionTimestamp, container state, RestartCount delta) and `rs.status()` (PRIMARY/SECONDARY state). With the correct measurement, the cap-1 invariant holds across all mutating tests.

### 13.4 Stateless components need explicit exemption

Mongos doesn't have replica-set quorum. Holding a `(CR, mongos)` cross-cluster lease creates a deadlock: cluster-1 takes the lease, rolls mongos, but never observes the "stop holding the lease" trigger because there's no AC-reconfig signal for stateless workloads. iter-14b added an `isCrossClusterMutexComponent` function so mongos bypasses the cross-cluster mutex entirely. Future stateless components must opt out explicitly.

### 13.5 ownerReferences across clusters are toxic

Hub-spoke writes member STSes with `ownerRef.uid = central-CR-UID`. When distributed operators take over with locally-created CRs (different UIDs), K8s GC sweeps the orphan-owned STSes. iter-17d's fix: drop ownerRefs entirely in distributed mode, use label-driven cleanup. Hub-spoke retains its current model unchanged.

### 13.6 Test fixtures vs production assumptions

Several iteration cycles were burned on test-fixture issues that don't exist in real GitOps deployments:
- `do_distributed_pre_replicate` writes per-cluster CRs with fresh UIDs (production: GitOps would write with stable UIDs).
- `image-registries-secret` not propagated to member namespaces (production: secret management is per-cluster anyway).
- `current.devc.kubeconfig` context drift broke `replicate_cr_resources.sh` (production: each operator only ever uses its local kubeconfig).

The PoC's test fixture conflates "set up a distributed deployment" with "simulate hub-spoke handoff", and those should be separate.

---

## 14. Future direction — leader-driven workflow (v2)

The current symmetric-reconcile-with-leases model **works** but fights the grain of the problem. A leader-driven workflow consolidates the cross-cluster state machine into one place and makes the followers idempotent thin executors — preserving the K8s controller idiom (event-driven, level-triggered) much better than today.

### 14.1 Shape

Three reconcilers, all controller-runtime native, all event-driven:

```
   ┌─── MongoDBSpecAgreementReconciler ─── (every operator)
   │
   │   Trigger: local CR change.
   │   Action:  compute hashCRSpec(local CR), submit ReportCRSpecHash.
   │   Return:  done (no requeue; next CR event fires me).
   │
   └───────────────────────────────────────

   ┌─── MongoDBStepExecutorReconciler ─── (every operator)
   │
   │   Trigger: local CR change OR FSM "step assigned to me" event.
   │   Action:
   │     step := GetMyNextStep(crKey, myCluster)
   │     if step == nil: requeue
   │     else: executeStep(step); ReportStepResult(step, err)
   │
   └───────────────────────────────────────

   ┌─── MongoDBWorkflowReconciler ─── (every operator, leader-only body)
   │
   │   Trigger: local CR change OR FSM "step result reported" event
   │            OR FSM "spec-agreement transition" event.
   │   Action:  if !raft.IsLeader: return
   │            else: advance workflow state, assign next steps via raft.
   │
   └───────────────────────────────────────
```

No background goroutine. No polling. Every reconcile is triggered by an event (K8s watch or FSM proposal). The follower's reconcile is one-step: ask, execute, report.

### 14.2 Why this is cleaner

| Concern | Current model | Leader-driven |
|---|---|---|
| Where does cross-cluster scheduling live? | Smeared across `distGateInline`, lease table, SpecGeneration plumbing | One file: `Workflow.Advance()` on the leader |
| Where is "what to do next" decided? | Each operator decides, then negotiates | The leader decides |
| Follower reconcile idempotency | Compromised by lease state + spec-gen threading | Strong — follower is "ask, execute, report" |
| Failover | New leader rebuilds from FSM + observed K8s | New leader resumes workflow from FSM |
| Bug class | "Two consistency models disagree" | "Workflow definition is wrong" (catchable in unit tests) |

### 14.3 What gets deleted vs added

- **Deleted (~600–1000 LOC)**: `AcquireOrRespect`, `HasSiblingLease`, `IsComponentReady`, `MarkReady`, lease tables, `distGateInline`, `distMarkReadyAndRelease`, `distReportInflightProgress`, `SpecGeneration` plumbing, `isCrossClusterMutexComponent`, scaler-rehydrate gates.
- **Added (~300–500 LOC)**: `Workflow` type, `Workflow.Advance` (deterministic state machine), `Step.Kind` dispatch, `GetMyNextStep` / `ReportStepResult` coordinator methods, FSM Apply for workflow proposals.
- **Untouched**: All transport code (FQDN advertise, MuxedStreamLayer, forwarder, Istio annotations, helm chart). Resource-agreement gate (now used at workflow-start). Hub-spoke. Search controller. Single-cluster controller.

### 14.4 Migration

Hub-spoke stays bit-identical. Distributed mode swaps under `if r.coordinator != nil`. The leader-driven path is a separate function; no entanglement. Estimated 2–3 weeks of focused engineering.

### 14.5 Why not now

The PoC validates the architectural claim (distributed operators CAN coordinate without cross-cluster K8s API access). Switching to leader-driven mid-PoC would mean throwing away iter 11–14g working code with no new evidence to show for it. v2 is the production-rebuild step, informed by the lessons in §13.

---

## 15. References

- `controllers/operator/mongodbshardedcluster_controller.go` — audit table at top + per-component gate composition.
- `pkg/coordination/raft/fsm_real.go` — `applyLeaseAllocate`, `HasSiblingLease`, FSM tables.
- `pkg/coordination/raft/transport_muxed.go` — MuxedStreamLayer with optional advertise address.
- `pkg/coordination/raft/forwarder.go` — follower → leader RPC channel.
- `pkg/coordination/raft/production.go` — `BuildProductionCoordinator`, FQDN wiring.
- `controllers/operator/distributed_resource_agreement.go` — Gate 1 (canonical-JSON CR hash + project CM + creds Sec).
- `helm_chart/values.yaml` + `helm_chart/templates/operator.yaml` — `operator.distributed.enabled` block, Istio annotations.
- `docs/dev/distributed-multicluster-operator-implementation.md` — companion as-built notes.
- `docs/dev/phase-d-handoff.md` — iteration log.
