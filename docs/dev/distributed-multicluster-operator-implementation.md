# Distributed MongoDB Operator — Implementation Notes

**Status:** PoC, validated end-to-end for sharded clusters in pod-mode on a 3-member kind multi-cluster mesh.
**Scope:** Describes how the PoC actually works after iter 11–14e. Companion to `distributed-multicluster-operator.md` (the original design proposal). Where this doc and the proposal disagree, this doc is correct.

---

## 1. The story in one page

Each Kubernetes cluster runs its own MongoDB operator pod. The operators agree on shared state via a **Raft cluster among themselves** — no central operator, no cross-cluster Kubernetes API. The user-authored MongoDB CR is replicated to every cluster (today via a test helper, in production via GitOps). Each operator watches its own cluster's CR copy and reconciles its own cluster's StatefulSets, Services, Secrets.

The Raft state machine carries the **runtime-authoritative shared state** that one operator cannot reconstruct alone:

- **Agreed resource hashes** — every operator must report the same content hash for the project ConfigMap and credentials Secret before any reconcile proceeds. This guarantees all clusters see the same desired state from CR.
- **Per-`(CR, component)` leases** — cross-cluster mutex that serialises StatefulSet writes for voting components (`configSrv`, `shard-N`) across clusters so replica-set quorum is never broken.
- **Component status** — which clusters have completed which step. Tied to `SpecGeneration` so a CR change invalidates stale "Ready" bits.

Only the leader writes to Ops Manager. Followers route OM-relevant proposals to the leader via a TCP forwarder on a sibling port. Local StatefulSet writes happen on each operator without going through the leader.

```
                         (user-authored CR, GitOps-distributed)
                                       │
              ┌────────────────────────┼────────────────────────┐
              ▼                        ▼                        ▼
       cluster-1 K8s API        cluster-2 K8s API        cluster-3 K8s API
         (MDB CR copy)            (MDB CR copy)            (MDB CR copy)
              │                        │                        │
        ┌─────┴─────┐            ┌─────┴─────┐            ┌─────┴─────┐
        │ operator  │            │ operator  │            │ operator  │
        │  cluster-1│            │  cluster-2│            │  cluster-3│
        │  (leader) │            │ (follower)│            │ (follower)│
        └─────┬─────┘            └─────┬─────┘            └─────┬─────┘
              │                        │                        │
              │      ┌──── raft 7000 ──┴───── raft 7000 ────┐    │
              └──────┤   app 7001 (forwarder follower→     │────┘
                     │              leader)                │
                     └─── Istio mTLS, mesh-traffic         ┘
                          excluded from sidecar
              │                        │                        │
              ▼                        ▼                        ▼
       local STS / Svc            local STS / Svc          local STS / Svc
       sh-0-1-{0..n}              sh-0-2-{0..n}            sh-0-3-{0..n}
       sh-config-1-{...}          sh-config-2-{...}        sh-config-3-{...}
       sh-mongos-1-0              sh-mongos-2-0            sh-mongos-3-0
```

---

## 2. What the PoC has proven

| Question | Answer |
|---|---|
| Per-cluster operators can coordinate via Raft without cross-cluster K8s API access | YES — pod-mode e2e green for deploy + rolling restart (`test_deploy_operator`, `test_create`, `test_sharded_cluster`, `test_rolling_restart`). |
| Cross-cluster replica-set safety invariant (`≤1 NotReady voting member globally`) is enforceable | YES for rolling restart. For multi-member scale-up the lease-release-per-+1-step semantics still leak; fix is in flight (iter 14e). |
| Leader-only OM writes work with follower forwarders | YES — `Forwarder.Submit` resolves leader FQDN from the FSM's peer list; iter-11 PeerAddrs map kept as defense-in-depth, iter-12 FQDN advertise made the wildcard branch a no-op. |
| Hub-spoke is unaffected | YES — every distributed gate is short-circuited via `r.coordinator == nil`. Hub-spoke remains the legacy path. |
| Disaster recovery (hub cluster lost) is automatic | YES in principle (any 2 of 3 operators form quorum, the missing cluster's state can be reconciled on return). Not yet end-to-end tested. |
| Hub-spoke → distributed takeover causes zero disruption (no STS rollout, no pod restart, no AC churn when distributed operators take over a hub-spoke deployment at steady state) | **NOT YET (iter 17c diagnostic)** — Phase B/C still PASS (deploy + scale-down hub-spoke + install distributed operators). Phase D FAILS with full STS recreation across 5 STSes (`sh-mongos-0`@c1, `sh-0-1`@c2, `sh-mongos-1`@c2, `sh-0-2`@c3, `sh-mongos-2`@c3) within the first 15s post-takeover; pods are recreated with new UIDs at t≈40s. iter-17c's rereading of the iter-17b log + post-mortem of `ls-1152` revealed the root cause is **NOT** the `deploymentState.SizeStatusInClusters` scaler short-circuit the iter-17b handoff described. The actual root cause is **K8s garbage-collector-driven STS deletion triggered by cross-cluster ownerReference UID mismatch**: hub-spoke writes STSes on each member cluster with `ownerReferences[0].uid = <central CR uid>`, but `do_distributed_pre_replicate` then creates a FRESH `MongoDB` CR on each member cluster with a server-assigned local UID. When the distributed operator's first reconcile fires, K8s GC sees the existing STS's ownerRef points to a uid that doesn't exist on the local API server and deletes the STS. The operator then recreates it with the local CR's uid as ownerRef — new STS uid, new pods, full disruption. iter-17c's planned `Status.SizeStatusInClusters` rehydrate fix would NOT help: GC fires before any reconcile code runs. iter-17d (next iter) needs to drop cross-cluster ownerReferences in distributed mode (or rewrite them at takeover). See `docs/dev/phase-d-handoff.md` "G'5 iter 17c status (2026-05-18)" for the full diagnostic — UIDs across all 4 clusters, the per-STS ownerRef trace, and the iter-17d scope discussion. |

---

## 3. What's hard / not yet solid

| Concern | Status |
|---|---|
| Test fixture replicates the CR to all clusters sequentially. CR spec is NOT in the resource-agreement gate. | KNOWN GAP. Today masked by `do_distributed_pre_replicate` in the test. For a real GitOps deployment, divergent CR generations across clusters can still create concurrent reconciles. Iter 14e is investigating whether this is the root of the scale-up cap violation. |
| Lease release granularity: lease releases after `+1` reconcile, not after the whole scale operation completes locally. Multi-member scale (`+3` per cluster) churns the lease ≥3 times per cluster, creating overlap windows where ≥2 clusters can hold the lease while their last-scaled pod is still NotReady. | KNOWN — iter-14e candidate fix is to hold the lease until `Status.ReadyReplicas == Spec.Replicas` for the local cluster. |
| Resource agreement covers only project ConfigMap + credentials Secret. TLS cert Secrets and LDAP/SCRAM bind Secrets are intentionally excluded (user-provided, may differ per cluster). | DESIGN CHOICE, documented in iter-8b commit. Acceptable for the PoC. |
| `MongoDBMultiCluster` controller is explicitly NOT registered in distributed mode. Only sharded is wired. | INTENTIONAL — replica-set and search controllers are out of PoC scope. |
| No persistent raft storage. Restart of a quorum-loss-recovered cluster requires re-bootstrap. | INTENTIONAL for the PoC. Replace with `raft-boltdb` for production. |
| Distributed operators are helm-installed under release name `mongodb-kubernetes-operator-multi-cluster`. The "multi-cluster" suffix is a misnomer in this mode (each operator watches a single local kubeconfig and coordinates over raft). | COSMETIC — rename queued post-PoC. |

---

## 4. The three substrates

| Substrate | Purpose | What lives there |
|---|---|---|
| **GitOps / Kubernetes etcd** | Durable user-intent | The MongoDB CR (`spec.shardCount`, `clusterSpecList`, etc.), TLS cert Secrets, LDAP Secrets, project ConfigMap, credentials Secret. Each cluster has its own copy; the test helper guarantees they're byte-identical at quiesce. |
| **Raft FSM** | Runtime shared state | Agreed-resource hashes, per-`(CR, component)` leases, per-`(CR, component)` status with `SpecGeneration`, raft membership. **Not durable across full quorum loss** — but durable enough for any single-cluster failure. |
| **Local Kubernetes API** | Per-cluster execution | Each operator's `kubectl get/apply` on its OWN cluster's STS, Svc, Pods. No operator reads or writes a sibling cluster's K8s API. |

Each substrate has a clean failure mode:
- Lose etcd → re-apply CR from git.
- Lose raft quorum → operators degrade to a "wait for quorum" state; do not write OM.
- Lose local kube API → that operator goes pending; others continue running their local clusters.

---

## 5. Raft mesh

### 5.1 Ports and transport

Each operator pod exposes two TCP ports inside the cluster, both behind a single ClusterIP Service per cluster:

```
+------- pod: mongodb-kubernetes-operator-{cluster-N} -------+
|                                                            |
|   listener 0.0.0.0:7000  ──┐                              |
|                            ├─► MuxedStreamLayer            |
|   listener 0.0.0.0:7001  ──┘   (handshake-byte demux)      |
|                                  │                         |
|                                  ├─► hashicorp/raft         |
|                                  │     (transport, FSM)     |
|                                  │                         |
|                                  └─► AppChannel             |
|                                         (Forwarder server)  |
|                                                            |
+------------------------------------------------------------+

Service: mongodb-kubernetes-operator-raft-{cluster-N}
  Port 7000  name=tcp-raft     appProtocol=tcp   → pod:7000
  Port 7001  name=tcp-raftapp  appProtocol=tcp   → pod:7001
```

`appProtocol: tcp` tells Istio "pass this through" so the sidecar doesn't HTTP-parse the raft frames. The pod template carries `traffic.sidecar.istio.io/excludeInboundPorts: "7000,7001"` and `excludeOutboundPorts: "7000,7001"` so the sidecar bypasses interception entirely for these flows.

### 5.2 Advertised address

The operator advertises an FQDN, not the wildcard bind:

```
RAFT_PEERS env on every operator (byte-identical across all 3 operators):
  mongodb-kubernetes-operator-raft-cluster-1.ls-1152.svc.cluster.local:7000,
  mongodb-kubernetes-operator-raft-cluster-2.ls-1152.svc.cluster.local:7000,
  mongodb-kubernetes-operator-raft-cluster-3.ls-1152.svc.cluster.local:7000

Each operator:
  1. Splits RAFT_PEERS by comma.
  2. Finds its own entry by matching ClusterName.
  3. Advertises that entry's address as its raft address.
  4. Listens on 0.0.0.0:7000 (and 0.0.0.0:7001) to accept connections from
     other operators dialling that FQDN.
```

This was iter 12's fix. Before iter 12, the leader's `LeaderWithID()` returned the listener's wildcard `[::]:7000`, and followers' forwarders ended up dialling `[::]:7001` which resolved to their own localhost. Iter 11 worked around this with a `PeerAddrs` map in the forwarder; iter 12 fixed it at the source so the workaround became defense-in-depth.

### 5.3 Cross-cluster reachability

Operators in different kind clusters reach each other via Istio multi-cluster's service-entry propagation. Once Istio mesh-bootstrap finishes (~10–30 seconds after operator startup), `mongodb-kubernetes-operator-raft-cluster-1.ls-1152.svc.cluster.local` resolves to cluster-1's ClusterIP service from any pod in cluster-2 or cluster-3. The DNS warm-up is the first thing each operator does — it retries dial for ~30 seconds before declaring quorum failure.

---

## 6. FSM state

The Raft state machine carries three tables:

```
┌──────────────────────── FSM state ────────────────────────┐
│                                                           │
│  AgreedResources                                          │
│  ┌────────────────────────────────────────────────────┐   │
│  │ CR-key       │ resource ref     │ cluster → hash   │   │
│  │ ───────────  │ ───────────────  │ ─────────────────│   │
│  │ ns/sh        │ CM/my-project    │ c1 → ed75eec…    │   │
│  │              │                  │ c2 → ed75eec…    │   │
│  │              │                  │ c3 → ed75eec…    │   │
│  │ ns/sh        │ Sec/my-creds     │ c1 → e4f0754…    │   │
│  │              │                  │ c2 → e4f0754…    │   │
│  │              │                  │ c3 → e4f0754…    │   │
│  └────────────────────────────────────────────────────┘   │
│                                                           │
│  ActiveLeases                                             │
│  ┌────────────────────────────────────────────────────┐   │
│  │ (CR, component)       │ holder cluster │ acquired  │   │
│  │ ──────────────────    │ ────────────── │ ────────  │   │
│  │ (ns/sh, configSrv)    │ kind-e2e-c1    │ T+0.42s   │   │
│  │ (ns/sh, shard-0)      │ —free—         │ —         │   │
│  │ (ns/sh, mongos)       │ kind-e2e-c1    │ T+0.45s   │   │  ← exempt
│  │                       │ kind-e2e-c2    │ T+0.45s   │   │  ← concurrent
│  │                       │ kind-e2e-c3    │ T+0.45s   │   │  ← OK for mongos
│  └────────────────────────────────────────────────────┘   │
│                                                           │
│  ComponentStatus                                          │
│  ┌────────────────────────────────────────────────────┐   │
│  │ (CR, component, cluster) │ Ready │ SpecGeneration  │   │
│  │ ──────────────────────── │ ───── │ ───────────────│   │
│  │ (ns/sh, configSrv, c1)   │ true  │ 5               │   │
│  │ (ns/sh, configSrv, c2)   │ true  │ 5               │   │
│  │ (ns/sh, configSrv, c3)   │ true  │ 4 ← STALE!      │   │
│  │ (ns/sh, shard-0, c1)     │ false │ 5               │   │
│  └────────────────────────────────────────────────────┘   │
│                                                           │
└───────────────────────────────────────────────────────────┘
```

Notes on each table:

- **AgreedResources** is filled by `ReportResourceHash` proposals from each operator after it reads its local copy of the referenced resource. The agreement test is: all known clusters have submitted a hash AND all hashes match. Until then, every operator's reconcile blocks at the top.

- **ActiveLeases** is the cross-cluster mutex from iter-13c. Lease key is `(CR, component)`. Apply-time invariant: `applyLeaseAllocate` rejects the request if any OTHER cluster currently holds the `(CR, component)` lease. The mongos exception from iter-14b: `isCrossClusterMutexComponent("mongos") == false`, so the reconciler short-circuits `AcquireOrRespect` for mongos and never proposes a `LeaseAllocate` for it — multiple clusters can roll their mongos in parallel.

- **ComponentStatus** carries the per-cluster Ready bit plus the `SpecGeneration` it was set at. iter-13b uses this: when a reconcile calls `IsComponentReady(currentSpecGen)`, the FSM returns `false` if the stored `SpecGeneration < currentSpecGen`. This is what makes a CR generation bump invalidate stale "Ready" assertions and force re-reconciliation.

---

## 7. Reconcile control flow

The shared reconcile entry point for a sharded MongoDB in distributed mode goes through three gates before it can mutate cluster state and one release path on exit:

```
   ┌─── Reconcile(MDB) ────────────────────────────────────────────┐
   │                                                                │
   │   1.  WaitForResourcesAgreed(MDB)                              │
   │       └─► report own hashes; block until all clusters agree    │
   │                                                                │
   │   2.  For each component (configSrv, shard-0, ..., mongos):    │
   │                                                                │
   │       2a. distGateInline(CR, component, currentSpecGen)        │
   │           ┌─► returns Proceed / Wait / SkipDone                │
   │           │                                                    │
   │           │   Proceed: this cluster has the lease (or          │
   │           │   doesn't need one), continue to 2b.               │
   │           │                                                    │
   │           │   Wait: another cluster holds the lease.           │
   │           │   Requeue and exit reconcile.                      │
   │           │                                                    │
   │           │   SkipDone: this cluster's slot is already         │
   │           │   Ready at currentSpecGen. Skip writes, continue.  │
   │           │                                                    │
   │           ▼                                                    │
   │       2b. mutate local STS / Svc for this component            │
   │                                                                │
   │       2c. if STS hits Ready:                                   │
   │             distMarkReadyAndRelease(CR, component,             │
   │                                     currentSpecGen)            │
   │             ── release lease + record Ready @ specGen          │
   │           else:                                                │
   │             distReportInflightProgress(CR, component, …)       │
   │             ── keep lease, refresh progress                    │
   │                                                                │
   │   3.  If leader: publish AutomationConfig to OM                │
   │       If follower: forward → leader via Forwarder.Submit       │
   │                                                                │
   │   4.  updateStatus(phase, message)                             │
   │       └─► ReportCRStatus proposal (every reconcile, leader     │
   │           or follower).                                        │
   │                                                                │
   └────────────────────────────────────────────────────────────────┘
```

The gates compose: **resource agreement** must hold before any per-component work runs. The **cross-cluster lease** mutates one cluster's STS at a time per voting component. The **SpecGeneration** invalidation ensures stale Ready bits don't cause a CR change to be ignored.

---

## 8. Simplified code snippets

These are pedagogical sketches, not literal source. Real call sites are `controllers/operator/mongodbshardedcluster_controller.go`, `pkg/coordination/raft/coordinator_impl.go`, `pkg/coordination/raft/fsm_real.go`.

### 8.1 Top-of-reconcile resource-agreement gate

```go
// At the top of the sharded reconciler, before any K8s mutation.
func (r *ShardedReconciler) Reconcile(ctx context.Context, ...) {
    if r.coordinator != nil {
        // 1. Report our local content hashes for the referenced resources.
        for _, ref := range collectSpecReferencedResourceRefs(r.sc) {
            hash := contentHash(r.localObject(ref))
            r.coordinator.ReportResourceHash(ctx, r.crKey(), ref, hash)
        }

        // 2. Block until ALL clusters have reported AND all hashes agree.
        if err := r.coordinator.WaitForResourcesAgreed(ctx, r.crKey()); err != nil {
            return requeue(err)
        }
    }
    // ... rest of reconcile
}
```

The set of referenced resources after iter-14e (Gate 0) — and the
canonical-JSON hash function refined by iter-14f:

```go
func collectSpecReferencedResourceRefs(sc *MongoDB) []ResourceRef {
    return []ResourceRef{
        // iter-14e: CR-spec agreement gate. Hash is computed over .spec
        // only (no status, no metadata.generation) so byte-identical
        // specs across clusters reach agreement regardless of per-cluster
        // K8s metadata drift. See `hashCRSpec` in
        // controllers/operator/distributed_resource_agreement.go.
        //
        // iter-14f: `hashCRSpec` now canonicalises the unstructured map
        // representation of `.spec` (sorted keys recursively) rather than
        // hashing `json.Marshal(sc.Spec)` directly. The typed-struct
        // marshal path was sensitive to pointer-vs-nil drift introduced
        // by InitDefaults, empty-map-vs-nil-map distinctions, and
        // omitempty quirks — sources of false drift even when the
        // wire-side JSON was byte-identical across clusters. Canonical
        // JSON depends ONLY on the value tree and therefore yields
        // identical bytes for two operators that decoded the same `.spec`.
        {Kind: "MongoDB",   Namespace: sc.Namespace, Name: sc.Name},
        {Kind: "ConfigMap", Name: sc.Spec.CloudManager.ConfigMapRef.Name},   // project CM
        {Kind: "Secret",    Name: sc.Spec.Credentials},                       // credentials
        // TLS cert Secrets and LDAP/SCRAM bind Secrets are NOT included —
        // user-provided, may legitimately differ per cluster (iter-8b).
    }
}
```

After iter-14f, when `gateOnResourceAgreement` returns `workflow.Pending`
(either path: local-read error or `ResourcesNotAgreed`), it FIRST emits a
keep-alive `ReportProgress` on every lease this cluster currently holds
for the CR (`refreshHeldLeases`). The FSM-side `applyStatusReport` path
refreshes the matching lease's `HeartbeatAt` as a side effect of every
status report from the holder, so the leader's stuck-step detector does
not revoke the lease while we're parked at the gate. Without this
refresh, a 60s+ spec-replication-lag window — `HeartbeatTTL` is 60s by
default — could break the cross-cluster cap=1 serialisation by aging
out an actively-held lease.

### 8.2 Per-component lease gate

```go
// distGateInline is called from each per-component write site (configSrv,
// each shard-N, mongos). It is the only place the cross-cluster lease is
// acquired.
func (r *ShardedReconciler) distGateInline(
    component string,
    currentSpecGen int64,
) distGateDecision {
    if r.coordinator == nil {
        return distGateProceed     // hub-spoke / single-cluster path
    }

    // Stateless components don't need cross-cluster mutex. iter-14b.
    if !isCrossClusterMutexComponent(component) {
        return distGateProceed
    }

    return r.coordinator.AcquireOrRespect(
        r.crKey(), component, r.myClusterName(), currentSpecGen,
    )
}

func isCrossClusterMutexComponent(c string) bool {
    return c != mongosComponentLabel  // mongos exempt; iter-14b.
}
```

### 8.3 FSM-side lease mutex

```go
// Inside the raft FSM, executed deterministically on every replica.
// iter-13c added the HasSiblingLease guard.
func (f *FSM) applyLeaseAllocate(p ProposalLeaseAllocate) ApplyResult {
    key := componentKey{CR: p.CR, Component: p.Component}

    // Cross-cluster mutex: reject if ANY OTHER cluster holds (CR, component).
    if other, held := f.HasSiblingLease(key, p.Cluster); held {
        return ApplyResult{Verdict: LeaseWait, Holder: other}
    }

    f.ActiveLeases[clusterKey{key, p.Cluster}] = leaseEntry{
        Holder: p.Cluster, Acquired: f.now(),
    }
    return ApplyResult{Verdict: LeaseHeld}
}

func (f *FSM) HasSiblingLease(key componentKey, ownCluster string) (string, bool) {
    for ck, _ := range f.ActiveLeases {
        if ck.componentKey == key && ck.Cluster != ownCluster {
            return ck.Cluster, true
        }
    }
    return "", false
}
```

### 8.4 SpecGeneration invalidation of stale Ready

```go
// iter-13b. ComponentStatus carries the SpecGeneration it was set at.
// A new CR generation invalidates older Ready bits.
func (f *FSM) IsComponentReady(
    crKey CRKey, component string, cluster string, currentSpecGen int64,
) bool {
    s, ok := f.ComponentStatus[statusKey{crKey, component, cluster}]
    if !ok || !s.Ready {
        return false
    }
    if currentSpecGen > 0 && s.SpecGeneration < currentSpecGen {
        return false      // stale Ready — treat as not-Ready
    }
    return true
}
```

### 8.5 Lease release vs in-flight progress

```go
// On every reconcile-exit code path, the controller either:
//   (a) marks the local component Ready @ currentSpecGen and releases
//       its lease (if it held one), or
//   (b) reports in-flight progress so the lease is retained and the
//       FSM knows another reconcile cycle is needed.
func (r *ShardedReconciler) distMarkReadyAndRelease(
    component string,
    currentSpecGen int64,
) {
    if r.coordinator == nil { return }
    r.coordinator.MarkReady(r.crKey(), component, r.myClusterName(),
                            ProgressSnapshot{
                                CRSpecGeneration: currentSpecGen,
                            })
    r.coordinator.ReleaseLease(r.crKey(), component, r.myClusterName())
}

// Critical: ONLY call when the local STS has truly converged for THIS
// component (Status.ReadyReplicas == Spec.Replicas, all updated, no
// in-flight rollout). Premature release breaks the cross-cluster
// invariant during multi-member scale.
//
//                ▲▲▲ This is exactly the iter-14e investigation ▲▲▲
```

### 8.6 Forwarder — follower routes OM-relevant proposals to leader

```go
// Submit blocks until the leader has applied the proposal or it times
// out. If THIS process is the leader, fast-path to local FSM. Otherwise
// dial the leader on its raft sibling port (7001) via the forwarder
// channel.
func (f *Forwarder) Submit(ctx context.Context, p Proposal) error {
    for attempt := 0; attempt < 30; attempt++ {
        leaderID, _ := f.raft.LeaderWithID()
        if leaderID == "" {
            time.Sleep(200 * time.Millisecond)
            continue
        }
        if leaderID == f.myID {
            return f.localApply(p)   // we ARE the leader
        }
        // iter-11 added PeerAddrs as a defense-in-depth; iter-12 made
        // LeaderWithID() return an FQDN directly, so PeerAddrs is now
        // a no-op safety net.
        addr := f.PeerAddrs[leaderID]
        if addr == "" {
            addr = leaderAdvertisedAddr(leaderID)  // fallback
        }
        // App-channel port = raft port + 1.
        appAddr := bumpPort(addr, +1)              // …:7000 → …:7001

        err := f.dialAndRoundtrip(ctx, appAddr, p)
        if err == nil { return nil }
        // back off, retry, possibly leadership changed mid-flight
    }
    return ErrSubmitExhausted
}
```

The forwarder is used only for proposals that affect shared state (`LeaseAllocate`, `MarkReady`, `ReportCRStatus`, `ReportResourceHash`). Local STS writes don't go through it.

---

## 9. Cluster-pass iteration with lease

When the reconciler walks per-component work, the lease is held by exactly one cluster at a time per `(CR, component)`. Multiple clusters DO reconcile concurrently — they just compete for each lease in turn:

```
Time →

cluster-1 reconcile:
  ├─ acquire (CR, shard-0)            ✓ HELD
  ├─ apply STS shard-0 cluster-1 spec
  ├─ wait STS ready: Spec=2, Ready=2
  ├─ MarkReady + release (CR, shard-0)

cluster-2 reconcile (running in parallel):
  ├─ acquire (CR, shard-0)            ✗ WAIT (cluster-1 holds)
  ├─ requeue → exit reconcile
  ┊                                   (cluster-1 finishes)
  ├─ acquire (CR, shard-0)            ✓ HELD
  ├─ apply STS shard-0 cluster-2 spec
  ├─ wait STS ready
  ├─ MarkReady + release

cluster-3 reconcile:
  ├─ acquire (CR, shard-0)            ✗ WAIT
  ┊                                   (cluster-2 finishes)
  ├─ acquire (CR, shard-0)            ✓ HELD
  ├─ apply STS
  ┊

mongos is exempt — all 3 clusters can roll mongos concurrently.
```

This is what makes the test's safety invariant — *≤1 NotReady voting member across all clusters at any moment* — hold for rolling restart. For multi-member scale-up by `+3 per cluster`, the same pattern needs to last across `+1, +1, +1` (three reconcile cycles within the same cluster) before any other cluster takes the lease. That's the iter-14e fix in flight: hold the lease until `Spec.Replicas == Status.ReadyReplicas == target`, not just per-reconcile.

---

## 10. Communication paths

### 10.1 Channels

| Channel | Direction | Carries | Wire |
|---|---|---|---|
| Raft transport | leader ↔ all peers | AppendEntries, RequestVote, InstallSnapshot | TCP 7000, hashicorp/raft framing, Istio mTLS |
| App-channel (forwarder) | follower → leader | OM-affecting proposals (resource-hash report, lease-allocate, mark-ready, CR-status, AC-publish trigger) | TCP 7001, length-prefixed msgpack, Istio mTLS |
| K8s watch | each operator | local CR copy, local STS status | local kube-API |

### 10.2 The mux

A single TCP listener on each port doesn't suffice because raft and app-channel use different framing. The PoC uses `MuxedStreamLayer` to demux by handshake byte:

```
  client connects → sends 1 byte:
                    0x01 → handed off to raft.NetworkTransport
                    0x02 → handed off to AppChannel server

  server reads first byte, dispatches the rest of the stream to the
  matching subsystem. After dispatch, the byte is consumed; subsequent
  bytes are normal protocol framing.
```

This was originally introduced to fit both raft and the forwarder onto a single port. In iter-7 the design was split across two ports (7000 raft, 7001 app-channel) but `MuxedStreamLayer` is kept because it makes the FQDN/advertise-addr handling identical for both ports.

### 10.3 Istio passthrough

Without intervention, Istio's sidecar intercepts every TCP connection on a labelled pod, tries to apply HTTP parsing or mTLS upgrade, and corrupts raft framing. iter-9 added two layers of protection:

1. Service port name `tcp-raft` / `tcp-raftapp` with `appProtocol: tcp` — tells Istio "treat this as opaque TCP".
2. Pod annotations `traffic.sidecar.istio.io/excludeInboundPorts: "7000,7001"` and `excludeOutboundPorts: "7000,7001"` — sidecar skips these ports entirely.

The combination is belt + braces. Without it, raft frames arrive corrupted at the peer and msgpack-decode errors flood the logs.

---

## 11. Gates summary

```
                   ┌─────────────────────────────┐
                   │ 0. CR-spec agreement gate   │  iter-14e
                   │    (.spec hash identical    │
                   │     across clusters)        │
                   └──────────────┬──────────────┘
                                  │ all clusters agree on spec?
                                  ▼
                   ┌─────────────────────────────┐
                   │ 1. resource-agreement gate  │
                   │    (project CM + creds Sec) │
                   └──────────────┬──────────────┘
                                  │ all clusters agree?
                                  ▼
                   ┌─────────────────────────────┐
                   │ 2. per-component lease gate │
                   │    (CR, component) mutex    │
                   │    mongos exempt            │
                   └──────────────┬──────────────┘
                                  │ lease held?
                                  ▼
                   ┌─────────────────────────────┐
                   │ 3. SpecGeneration check     │
                   │    (skipDone if stored ≥    │
                   │     current; else proceed)  │
                   └──────────────┬──────────────┘
                                  │
                                  ▼
                       mutate local STS/Svc

                                  │
                                  │  STS ready?
                                  ▼
                   ┌─────────────────────────────┐
                   │ 4. release + Ready @ specGen│
                   │    or refresh in-flight     │
                   └─────────────────────────────┘

                                  ▼
                   ┌─────────────────────────────┐
                   │ 5. forwarder to leader      │
                   │    for OM writes only       │
                   └─────────────────────────────┘
```

Gates 0 and 1 share the same underlying `collectSpecReferencedResourceRefs`
+ `gateOnResourceAgreement` plumbing — the iter-14e change adds the
MongoDB CR itself (via `CRSpecResourceKind = "MongoDB"` and `hashCRSpec`,
both in `controllers/operator/distributed_resource_agreement.go`) to
the agreement set. The hash is computed over `.spec` only (no
`.status`, no `.metadata.generation`, no `managedFields`) so two
clusters with byte-identical specs reach agreement even when their
per-cluster K8s metadata diverges (which it always does — apiservers
have independent resourceVersion counters).

Operationally cheap: one extra raft heartbeat per reconcile (the
content hash is small). Architecturally large: the gate replaces
"whichever cluster happens to be raft leader wins" with "no one moves
until all clusters' watches have caught up". This closes the iter-14e
race where one cluster's CR-watch fired on a scale-up mutation, the
local operator started reconciling immediately against
`members=target`, and the other clusters were still reconciling
against `members=baseline` because their CRs hadn't been written yet.

---

## 12. Failure modes

### 12.1 One cluster fails

- Lose a follower: raft retains quorum (2/3), leader continues. The lost cluster's `ComponentStatus` rows go stale; once the FSM detects the peer-link timeout (≈10s with production raft config), it stops counting it toward agreement. Followers reconciling continue. New writes succeed.
- Lose the leader: raft elects a new leader (term-bump within ≈1–3s). The forwarder retries with the new leader. Reconciles in progress requeue and resume on the new leader.

### 12.2 Two clusters fail

- Quorum lost. Raft refuses to apply any write proposal. Each surviving operator's `WaitForResourcesAgreed` blocks indefinitely. STSes already running keep running (Kubernetes doesn't depend on the operator post-deploy). When a second cluster comes back, raft re-elects and reconciles resume.

### 12.3 Partition

- Minority partition: same as "quorum lost". Operator is read-only.
- Majority partition: continues writing. When the partition heals, the minority side's logs are overwritten by the majority's.

### 12.4 Raft state lost (entire fleet restart with no persistent storage)

- Today's PoC has no persistent raft storage. Full fleet restart loses the FSM. The CR remains in each cluster's etcd (durable); operators re-bootstrap raft, re-report resource hashes, and converge. Ops Manager retains the active AC so no actual rolling restart of mongod pods occurs.

---

## 13. Implementation status table

| Component | File | Status |
|---|---|---|
| `MuxedStreamLayer` w/ FQDN advertise | `pkg/coordination/raft/transport_muxed.go` | ✓ |
| Forwarder + PeerAddrs map | `pkg/coordination/raft/forwarder.go` | ✓ |
| `BuildProductionCoordinator` | `pkg/coordination/raft/production.go` | ✓ |
| FSM lease + cross-cluster mutex | `pkg/coordination/raft/fsm_real.go` | ✓ |
| FSM `ComponentStatus` + `SpecGeneration` | `pkg/coordination/raft/state.go`, `proposals.go` | ✓ |
| Resource-agreement gate (narrow) | `controllers/operator/distributed_resource_agreement.go` | ✓ |
| `distGateInline` per-component | `controllers/operator/mongodbshardedcluster_controller.go` | ✓ |
| `distMarkReadyAndRelease` / `distReportInflightProgress` | same | ✓ |
| mongos exemption | same (`isCrossClusterMutexComponent`) | ✓ |
| Helm chart `operator.distributed.enabled` + Service | `helm_chart/values.yaml`, `helm_chart/templates/operator.yaml` | ✓ |
| Istio sidecar excludes ports | same (pod-template annotations) | ✓ |
| Scaler rehydrate (gated by live local STS + spec seed for remotes, anchored to Status.ReadyReplicas) | `controllers/operator/mongodbshardedcluster_controller.go:initializeMemberClusters` + `rehydrateReplicasFromLiveStatefulSets` | iter 14e ✓ |
| **CR-spec agreement gate (Gate 0)** — CR `.spec` in agreed-set, canonical-JSON hash | `controllers/operator/distributed_resource_agreement.go` (`CRSpecResourceKind`, `hashCRSpec` + `canonicalJSON` / `canonicalise`, expanded `collectSpecReferencedResourceRefs`) | **iter 14e ✓ (initial)** / **iter 14f ✓ (canonical-JSON hash via unstructured map; immune to typed-struct marshal drift)** |
| Lease keep-alive during top-of-reconcile gate wait | `controllers/operator/distributed_resource_agreement.go` (`refreshHeldLeases`), `pkg/coordination/coordinator.go` (`GetLeasesHeldBy` on `DistributedCoordinator`), `pkg/coordination/raft/fsm_real.go` / `coordinator_impl.go` (`GetLeasesHeldBy` impl) | **iter 14f ✓** — `gateOnResourceAgreement` calls `refreshHeldLeases` before any `workflow.Pending` so the FSM-side `HeartbeatAt` is refreshed on every lease this cluster holds. Prevents leader-side `HeartbeatTTL`-based revocation while the holder is parked at a top-of-reconcile gate (CR-spec replication lag, transient read error). |
| Lease-release per-scale-operation (hold lease through entire +N until ReadyReplicas catches up) | `controllers/operator/mongodbshardedcluster_controller.go:createOrUpdateShards` (existing `distGateInline` + `distMarkReadyAndRelease` paths, paced by `Status.ReadyReplicas` anchor) | iter 14e ✓ via ReadyReplicas anchor — explicit per-scale lease still TODO post-PoC |
| Persistent raft storage | — | NOT IMPLEMENTED (intentional for PoC) |
| `MongoDBMultiCluster` controller wired | — | DEFERRED (sharded only) |
| Hub-spoke → distributed takeover test | `docker/mongodb-kubernetes-tests/tests/multicluster_shardedcluster/multi_cluster_sharded_takeover.py` | **iter 17c BLOCKED on cross-cluster ownerReference UID mismatch** — Phase B+C still GREEN (deploy + scale-down + install distributed operators). Phase D FAILS within ~15s post-takeover with 5 STSes UID-changing simultaneously across all 3 member clusters; pod UIDs change at t≈40s onward. iter-17c's diagnostic identified that the failure is K8s-GC-driven STS deletion, not the scaler short-circuit the iter-17b handoff described. Hub-spoke writes STSes with `ownerReferences[0].uid = <central CR uid>` on each member cluster; `do_distributed_pre_replicate` then creates a fresh `MongoDB` CR on each member with a different server-assigned uid; K8s GC on each member cluster deletes the existing STSes because the ownerRef is now unresolvable. iter-17c's planned `Status.SizeStatusInClusters` rehydrate fix would not help because GC fires before any reconcile code runs. iter-17d (next iter) needs to drop cross-cluster ownerReferences in distributed mode (`construct.DatabaseStatefulSet` gated on `r.coordinator != nil` AND `memberCluster != central`) or rewrite them at takeover. See `docs/dev/phase-d-handoff.md` "G'5 iter 17c status (2026-05-18)". |
| **Takeover deploymentState rehydrate** (iter-17c original scope) | `controllers/operator/mongodbshardedcluster_controller.go:initializeMemberClusters` + `rehydrateReplicasFromLiveStatefulSets` | **iter 17c NOT NEEDED** — the iter-17c scope (rehydrate `Status.SizeStatusInClusters` from live STSes on a fresh operator's first reconcile) was already covered by iter-14e's `rehydrateReplicasFromLiveStatefulSets` (gated on `r.coordinator != nil` + per-component local-STS gate). The unit tests `TestDistributedMode_FollowerScalerOneAtATime`, `TestDistributedMode_FollowerScaleUpStaircaseWithoutShardCount`, `TestDistributedMode_FollowerScalerAnchorsToReadyReplicas` pin the rehydrate behaviour for the post-PhaseRunning paths AND for the `Status.ShardCount=0` path. They pass on tip `5f687a632` (iter-17b doc commit). iter-17c's planned added test would have asserted the same behaviour at a different layer; landing it would not change the takeover outcome because the GC-driven STS deletion happens BEFORE any reconcile code runs. |
| Distributed pod-mode — skip cross-cluster client init | `main.go` (lines 238-285 → `RAFT_PEERS`-gated branch), `pkg/multicluster/multicluster.go:ClustersMapToClientMap`, `controllers/operator/appdbreplicaset_controller.go:createMemberClusterListFromClusterSpecList` (two sites: spec branch + previous-member branch), `controllers/operator/mongodbopsmanager_controller.go:NewOpsManagerReconcilerHelper`, `controllers/operator/mongodbsearch_controller.go:AddMongoDBSearchController`, `controllers/operator/mongodbsearchenvoy_controller.go:AddMongoDBSearchEnvoyController`, `pkg/telemetry/collector.go:RunTelemetry` | **iter 17b ✓** — when `RAFT_PEERS` is set, `memberClusterObjectsMap` is populated with NIL entries for peer cluster names (name known, no K8s client). All map iterations across the codebase now nil-skip these entries so the operator never attempts to register controller-runtime against unreachable cross-cluster API servers. Unit tests `TestDistributedPodMode_NilClientForPeerClusters_AppDBHelper` and `TestDistributedPodMode_NilClientForPeerClusters_PreviousMembers` pin the controller-side invariant. Hub-spoke is unaffected (`RAFT_PEERS` unset → unchanged code path; nil-skip branches unreachable because every populated entry has a real client). Commits: `36a6e4231` (tests), `5087aa26c` (core fix), `a27083616` (search/envoy nil-skip), `caf5582b1` (telemetry nil-skip). |
| **Per-RS safety monitor — combined pod-lifecycle ∪ rs.status()** | `docker/mongodb-kubernetes-tests/tests/multicluster_shardedcluster/multi_cluster_sharded_simplest.py` (`_run_safety_monitor`, `_pod_lifecycle_serving`, `_query_rs_status`, `_rs_member_states`) | **iter 14g ✓** — Union of two signals: K8s pod-lifecycle (phase=Running ∧ no deletionTimestamp ∧ mongod container Running ∧ no restartCount delta) AND rs.status() member state ∈ {PRIMARY,SECONDARY} ∧ health=1. K8s pods missing from the rs.status() member set are excluded from the count (mid-add / mid-remove — not voting members of the RS). Plus a post-step quiesce check (poll up to 120s) that every RS member is PRIMARY/SECONDARY. The legacy K8s-readiness monitor stays as informational diagnostic — its cap=1 was producing false positives during AutomationAgent AC reloads (iter-14f finding). |
| `test_rolling_restart` — pod-mode | same | iter-14g ✓ (also iter-14d/e/f); **iter-14h ✓ EVG-GREEN** at `6a0a28290f01f60007fa9f7d` (Phase Running reach took 2078s within the new 2400s budget; safety `max_out_per_component={configSrv: 1, shard-0: 1}`). |
| `test_scale_up_3` — pod-mode | same | **iter 14g ✓** — local pod-mode GREEN with the new safety monitor; iter-14e/14f's cap-violation signature was a measurement artefact (K8s-readiness flicker during agent AC reload on non-scaling clusters), not a coordinator-state bug. **iter-14h ✓ EVG-GREEN** at the same patch (1365s; safety `max_out={shard-0: 1}`; quiesce: configSrv 5 + shard-0 14 all PRIMARY/SECONDARY). |
| `test_scale_down_3` — pod-mode | same | **iter 14g ✓** — first GREEN run with the combined monitor. **iter-14h ✓ EVG-GREEN** at the same patch (1096s; safety `max_out={shard-0: 1}`; both 5 members PRIMARY/SECONDARY). |
| Phase 4 — EVG remote e2e on the full distributed-pod-mode test (6/6) | `docker/mongodb-kubernetes-tests/tests/multicluster_shardedcluster/multi_cluster_sharded_simplest.py` against `OVERRIDE_VERSION_ID=6a09c8fa29ac5d000772c2ba` (iter-14f image) | **iter 14h ✓ GREEN** — patch `6a0a28290f01f60007fa9f7d` 6/6 PASS. iter-14h bumped `assert_reaches_phase` + `_run_safety_monitor` timeouts from 1500s to 2400s on all 3 mutating tests; EVG-vs-local wall-clock disparity (~1.7-2x) confirmed (`test_rolling_restart` took 2078s on EVG vs ~14 min locally). Safety monitor reported zero quorum violations across all three mutating tests on EVG — same `max_out_per_component` values as the local pod-mode run. |

---

## 14. Reading guide

If you only have time for three files:

1. `controllers/operator/mongodbshardedcluster_controller.go` — the audit table at the top documents which write sites are leader-gated, which are local. The reconcile function shows the gate composition. Search for `distGateInline`, `distMarkReadyAndRelease`, `r.coordinator`.

2. `pkg/coordination/raft/fsm_real.go` — `applyLeaseAllocate` and `applyMarkReady` show the FSM invariants. `HasSiblingLease` is the cross-cluster mutex.

3. `controllers/operator/distributed_resource_agreement.go` — `collectSpecReferencedResourceRefs` and `WaitForResourcesAgreed`. Small file. Explicit about what is and isn't gated.

The handoff log `docs/dev/phase-d-handoff.md` is the iteration narrative — each iter section explains a single root cause and its fix in commit-message detail. Useful when something looks weird and you want to know why it's that way.
