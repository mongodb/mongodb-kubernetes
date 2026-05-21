# Per-Cluster MongoDB Operators with Raft-Coordinated Consensus

**A multi-cluster architecture without cross-cluster Kubernetes API access.**

**Status:** PoC validated end-to-end on Evergreen for sharded clusters in distributed pod-mode. The full multi-cluster sharded e2e suite (deploy, create, sharded-cluster, rolling restart, scale up by three voting members per cluster, scale down by three voting members per cluster) passes on Evergreen with the cross-cluster safety invariant enforced. Hub-spoke deployments remain unaffected by the new code paths. The hub-spoke-to-distributed takeover migration scenario was substantially worked through and the zero-disruption property has been demonstrated on the swap event itself; the end-to-end takeover demo is still pending on a small test-fixture infrastructure item.

**Scope:** Architectural direction plus what was actually built and what was learned. Replaces the single-central-operator (hub-and-spoke) model for multi-cluster MongoDB deployments. Hub-and-spoke remains the default and ships unchanged.

**Companion docs:** `distributed-multicluster-operator-implementation.md` (as-built notes with code snippets and the FSM state tables in detail), `phase-d-handoff.md` (development log).

---

## 1. One-page summary

Each Kubernetes cluster runs its own MongoDB operator pod. The operators agree on shared state via a Raft cluster amongst themselves. There is no central operator and no cross-cluster Kubernetes API access. The user-authored MongoDB CR is replicated to every cluster (in production via GitOps; in the PoC test fixture via a helper). Each operator watches its own cluster's CR copy and reconciles only its own cluster's StatefulSets, Services, and Secrets.

The Raft state machine carries the runtime-authoritative shared state that no single operator can reconstruct alone:

- A table of agreed resource hashes — the project ConfigMap, the credentials Secret, and a stable hash of the MongoDB CR spec. Each operator submits its own observation; reconcile blocks at the top until every operator has reported the same hash for each resource.
- A table of active per-`(CR, component)` cross-cluster leases. At most one cluster writes the StatefulSet for `configSrv` or any `shard-N` at a time, so the replica-set quorum invariant ("at most one voting member down globally per replica set") holds across clusters.
- A table of per-`(CR, component, cluster)` status entries, each carrying the CR spec generation at which it was last set. A new CR generation invalidates older "Ready" status; the operator does not consider stale status a reason to skip a reconcile.

Only the leader writes to Ops Manager. Followers route OM-relevant proposals to the leader through a forwarder running on a sibling TCP port. Local StatefulSet writes happen on each operator directly, without leader involvement.

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

A single central operator runs in a designated "hub" cluster. The hub holds a Secret with kubeconfigs for every member cluster and reads/writes member StatefulSets, Services, and Secrets directly across cluster boundaries. For replica-set ordering, the hub publishes a merged AutomationConfig to Ops Manager; OM's in-pod automation agents then enforce one-voting-member-change-at-a-time. Cross-cluster StatefulSet rollout is explicitly serialised inside the hub controller (see `controllers/operator/mongodbmultireplicaset_controller.go:568-572`).

### 2.2 Why this is a problem

A single central operator creates a number of operational and security issues that customers have asked us to address.

1. **The hub cluster is a single point of failure.** If that cluster becomes unavailable, the deployment cannot be reconciled. Recovery is a manual process: re-install the operator, the CRDs, the kubeconfigs, the certificates, and the CRs in a new cluster. There is no automated failover path today.

2. **Cross-cluster Kubernetes API access is a hard constraint for customers.** Several customers explicitly refuse to open one cluster's API server to another cluster on security and compliance grounds. Hub-and-spoke requires the hub to talk to every member cluster's API server, which is exactly what customers will not accept.

3. **Ops Manager is a hard runtime dependency.** The current control plane assumes OM is reachable for every AutomationConfig publication. The longer-term direction is for OM to be optional, with the operator able to manage workloads even without it.

### 2.3 Constraints from customers

Three constraints are non-negotiable:

- No cross-cluster Kubernetes API connectivity in production deployments.
- All existing replica-set safety invariants must continue to hold (member-down rule, voting-member-reconfig serialisation).
- Hub-and-spoke installations must continue to ship unchanged. Distributed mode is an opt-in, parallel deployment shape.

---

## 3. The three substrates

The system maintains three independent data substrates, each with a different consistency model and a different failure mode. They are designed to be separable: loss of one substrate does not propagate to the others.

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
│    do not write OM. Running mongods unaffected.                 │
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

The intent of the three-substrate split is operational: each kind of failure has a single recovery path. Losing the Raft quorum does not kill running mongods, because the mongod processes do not depend on the operator after deployment. Losing the local Kubernetes API on one cluster does not affect the other clusters' operators, because they only ever talk to their own cluster's API.

---

## 4. Goals and non-goals

### Goals

- Eliminate cross-cluster Kubernetes API connectivity as a deployment requirement.
- Eliminate the central-cluster single point of failure; any surviving cluster can continue management.
- Preserve all replica-set safety invariants. Specifically, no more than one voting member down globally per replica set at any moment during rolling restarts and scale operations.
- Provide a migration path from hub-and-spoke to distributed mode that does not disrupt running mongods (no StatefulSet rollout, no pod restart triggered by the operator swap).
- Lay the foundation for OM-optional deployments in a later phase.

### Non-goals

- Replacing Ops Manager as the source of truth for users, monitoring, and backups. Those remain OM concerns.
- Tolerance against adversarial operator behaviour. The Raft library used here assumes crash-stop, non-adversarial peers.
- Redesigning the cross-cluster service mesh. The PoC uses Istio multi-cluster; other mesh choices would work with equivalent passthrough configuration.
- Redesigning the CR schema beyond minimal additions for cluster lists where needed.

---

## 5. Architecture

### 5.1 One operator binary, two modes

The same operator binary supports both hub-and-spoke and distributed mode. The choice is made at install time, via a Helm value:

```
operator.distributed.enabled = false    # hub-and-spoke (default)
operator.distributed.enabled = true     # per-cluster operator, raft-coordinated
```

When `distributed.enabled` is false, the operator's `coordinator` field is nil. Every distributed-mode code path short-circuits to the legacy hub-and-spoke logic when the coordinator is nil. The audit table at the top of `controllers/operator/mongodbshardedcluster_controller.go` enumerates every write site and documents which gate applies in distributed mode.

### 5.2 Raft mesh

Each operator pod exposes two TCP ports inside its cluster, each dedicated to a single purpose. The ports are served by separate listeners and have no overlap. They sit behind a single per-cluster ClusterIP Service.

```
+--------- pod: mongodb-kubernetes-operator-{cluster-N} -----------+
|                                                                  |
|   0.0.0.0:7000  ─────► hashicorp/raft NetworkTransport           |
|                        (AppendEntries, RequestVote, InstallSnap) |
|                                                                  |
|   0.0.0.0:7001  ─────► AppChannel server (Forwarder)             |
|                        (follower → leader RPC)                   |
|                                                                  |
+------------------------------------------------------------------+

Service: mongodb-kubernetes-operator-raft-{cluster-N}
  Port 7000  name=tcp-raft     appProtocol=tcp   → pod:7000
  Port 7001  name=tcp-raftapp  appProtocol=tcp   → pod:7001
```

Port 7000 carries the Raft protocol itself, including leader election, log replication, and snapshot installation. Port 7001 carries application-level RPCs between followers and the leader: requests to allocate a lease, to mark a component Ready, to report a CR status, and to publish an AutomationConfig. The two channels are kept on separate ports so that Raft's framing and the application's framing never have to be disambiguated on the same wire.

### 5.3 FQDN advertisement

Operators advertise an FQDN address, not their wildcard bind address, for the purpose of peer-to-peer communication. The set of peer addresses is provided to every operator pod as the `RAFT_PEERS` environment variable. The value is byte-identical across all operators:

```
RAFT_PEERS = mongodb-kubernetes-operator-raft-cluster-1.ls-1152.svc.cluster.local:7000,
             mongodb-kubernetes-operator-raft-cluster-2.ls-1152.svc.cluster.local:7000,
             mongodb-kubernetes-operator-raft-cluster-3.ls-1152.svc.cluster.local:7000
```

On startup each operator splits `RAFT_PEERS`, finds its own entry by matching the `RAFT_CLUSTER_NAME` field of each address, and uses that entry's address as its advertised address. The other two entries become its raft peers. The operator listens on `0.0.0.0:7000` and `0.0.0.0:7001` so it accepts connections from peers dialling its FQDN.

The reason FQDN advertisement matters: if an operator advertised its wildcard bind address (`[::]:7000`), peers would receive a leader address they cannot resolve from outside the leader's own host network namespace. They would either retry forever or, worse, attempt to dial the address and end up connected to themselves on their own localhost.

### 5.4 Istio multi-cluster mesh

Operators in different Kubernetes clusters reach each other across cluster boundaries via Istio's multi-cluster service-entry propagation. Once the mesh has propagated the per-cluster Service, DNS resolution for `mongodb-kubernetes-operator-raft-cluster-N.<ns>.svc.cluster.local` works from any pod in any of the three clusters.

Two layers of protection ensure the Istio sidecar passes raft and forwarder frames through unmodified, rather than attempting HTTP parsing or mTLS upgrade that would corrupt the wire format:

1. The Service ports are named `tcp-raft` and `tcp-raftapp`, both with `appProtocol: tcp`. This tells Istio to treat the ports as opaque TCP.
2. The operator pod template carries `traffic.sidecar.istio.io/excludeInboundPorts: "7000,7001"` and `traffic.sidecar.istio.io/excludeOutboundPorts: "7000,7001"`. This causes the sidecar to bypass interception entirely for these ports.

The combination is intentional. The `appProtocol: tcp` declaration is enough on its own to make the mesh leave the bytes alone, but the sidecar still proxies the connection. The exclude annotations remove the sidecar from the path for these specific ports, which both reduces overhead and removes a source of intermittent failure during sidecar config reloads.

---

## 6. FSM state

The Raft state machine carries three tables. Each `Apply` is deterministic, so all replicas converge on the same state.

```
┌─────────────────────────── FSM ────────────────────────────┐
│                                                            │
│  AgreedResources                                           │
│  ┌──────────────────────────────────────────────────────┐  │
│  │ CR-key    │ resource ref       │ cluster → hash     │  │
│  │ ────────  │ ─────────────────  │ ──────────────────│  │
│  │ ns/sh     │ MongoDB/sh (spec)  │ c1 → ab12cd…       │  │
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
│  │ (ns/sh, mongos)    │ —not held—     │ EXEMPT         │  │
│  │                    │                │ (stateless)    │  │
│  └──────────────────────────────────────────────────────┘  │
│                                                            │
│  ComponentStatus                                           │
│  ┌──────────────────────────────────────────────────────┐  │
│  │ (CR, component, cluster) │ Ready │ SpecGeneration   │  │
│  │ ────────────────────────  │ ───── │ ─────────────── │  │
│  │ (ns/sh, configSrv, c1)   │ true  │ 5                │  │
│  │ (ns/sh, configSrv, c2)   │ true  │ 5                │  │
│  │ (ns/sh, configSrv, c3)   │ true  │ 4 ← STALE        │  │
│  │ (ns/sh, shard-0, c1)     │ false │ 5                │  │
│  └──────────────────────────────────────────────────────┘  │
│                                                            │
└────────────────────────────────────────────────────────────┘
```

**AgreedResources** is populated by every operator's `ReportResourceHash` proposal. After each operator reads its local copy of a tracked resource (the MDB CR's `.spec` subtree, the project ConfigMap, the credentials Secret), it submits its observed content hash. Reconcile blocks at the top of the loop until every known cluster has reported a hash for each tracked resource and all hashes match. The CR hash uses a canonical JSON encoding of the `.spec` subtree, so identical specs across clusters produce identical hashes even when individual clusters carry different `metadata.generation` values or different `metadata.managedFields` records.

**ActiveLeases** is the cross-cluster mutex for components that have replica-set quorum semantics. The lease key is `(CR, component)`. When an operator wants to write its local StatefulSet for `(CR, component)`, it proposes a `LeaseAllocate` to Raft. The FSM applies the proposal and inspects the lease table: if any other cluster currently holds the `(CR, component)` lease, the proposal is rejected. Otherwise the lease is granted. The mongos component is excluded from this mechanism because it is stateless — multiple clusters can roll their mongos pods in parallel without breaking quorum.

**ComponentStatus** records, for each `(CR, component, cluster)`, whether the cluster has reached steady state for that component, and at which CR spec generation. When a reconcile runs at generation N+1, it treats any Ready row stored at a generation lower than N+1 as stale and forces re-reconciliation regardless of the Ready bit. This is the mechanism by which a CR change causes operators to re-evaluate components they already considered "done" at an earlier generation.

---

## 7. The gates

The distributed reconcile path is a small composition of gates. Each gate produces one of three outcomes: Proceed (continue with the next step), Wait (requeue this reconcile cycle), or SkipDone (the work for this generation has already been done by this cluster, skip and move on).

```
   ┌─────────────────────────────────────────────────────┐
   │  Gate 1: resource-agreement                         │
   │                                                     │
   │  Has every cluster reported the same hash for:      │
   │    • MDB CR .spec  (canonical JSON)                 │
   │    • project ConfigMap                              │
   │    • credentials Secret                             │
   │                                                     │
   │  TLS / LDAP secrets are deliberately excluded —     │
   │  user-provided, may legitimately differ per         │
   │  cluster.                                           │
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
   │  Mongos: bypass — return Proceed unconditionally.   │
   │  Stateless, no quorum semantics.                    │
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
   │  The lease is held until the local cluster reaches  │
   │  the target replica count for this component, not   │
   │  released per per-reconcile increment.              │
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

Hub-and-spoke installations skip Gates 1 through 4 entirely. The legacy code path runs unchanged. The same Kubernetes mutation code (StatefulSet apply, Service apply, etc.) executes in both modes; only the gating around it differs.

---

## 8. Reconcile flow

In code, the distributed-mode reconcile decorates the existing K8s-mutation logic with the gates from the previous section. Simplified:

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

The Kubernetes mutation code (`createOrUpdateSTS`, `publishAutomationConfig`, and so on) is the same code that runs in hub-and-spoke. The gates are the only addition. The intent of this structure is for the distributed-mode logic to be a thin wrapper rather than a parallel implementation, so behaviour on the inside of the gates remains consistent between the two modes.

---

## 9. Cross-cluster serialisation by lease

The cross-cluster lease is what guarantees the "at most one voting member down globally per replica set" invariant. The mechanism is straightforward: each cluster's reconcile attempts to acquire the `(CR, component)` lease before writing the local StatefulSet for that component. The Raft FSM serialises lease grants — at most one cluster holds any given `(CR, component)` lease at any moment.

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

At any moment during a voting-component roll, at most one cluster's StatefulSet is mutating. The within-cluster StatefulSet RollingUpdate strategy enforces "one pod down at a time" inside each cluster. The two mechanisms compose: at most one voting member down per cluster (StatefulSet RollingUpdate), combined with at most one cluster rolling at a time (cross-cluster lease), gives at most one voting member down globally per replica set.

Mongos has no quorum semantics. A cross-cluster mutex on mongos would also be incorrect for a different reason: mongos rolling does not produce an `rs.reconfig` event, so the natural "lease release on reconfig completion" trigger does not fire for mongos. Mongos is therefore bypassed at Gate 2 — every cluster's reconcile is allowed to roll its mongos in parallel.

---

## 10. Communication

The three communication paths in the system carry different kinds of information and use different transports.

| Channel | Direction | Carries | Wire |
|---|---|---|---|
| Raft transport | leader ↔ all peers | AppendEntries, RequestVote, InstallSnapshot | TCP 7000, hashicorp/raft framing, Istio mTLS passthrough |
| App-channel (forwarder) | follower → leader | OM-affecting proposals: ResourceHash, LeaseAllocate, MarkReady, CRStatus, ACPublish | TCP 7001, length-prefixed msgpack, Istio mTLS passthrough |
| K8s watch | each operator | local CR copy, local STS status | local kube API only |

The forwarder is used only for proposals that need to affect FSM state. Local StatefulSet writes never go through the forwarder — each operator writes its own cluster's StatefulSets directly via its local Kubernetes client.

```
   follower (cluster-2)                          leader (cluster-1)
   ─────────────────────                          ─────────────────

   coordinator.Submit(LeaseAllocate)
   │
   ├─ resolve leader: LeaderWithID() returns
   │  cluster-1's FQDN
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

The Raft library exposes the leader's identity via `LeaderWithID`. Because operators advertise FQDN addresses (see section 5.3), the address returned by `LeaderWithID` resolves correctly from any peer. The forwarder dials the leader's app-channel port (raft port + 1) and sends a length-prefixed msgpack frame containing the proposal. The leader's AppChannel server applies the proposal to the local FSM via `raft.Apply`, which replicates to the followers and produces a deterministic result. The result is returned to the follower as a length-prefixed msgpack ack.

---

## 11. The takeover scenario

The strongest correctness test for the design is the migration scenario: take a healthy hub-and-spoke deployment in steady state, swap to distributed operators, and observe zero disruption to running mongods. No StatefulSet recreation, no pod restart, no AutomationConfig change. Just a continuity of running workload across the operator swap.

```
   t=0   Hub-and-spoke deployment running, MDB CR Phase=Running.
         Central operator on kind-e2e-operator owns the StatefulSets on
         all three member clusters. Each STS has an ownerReference
         pointing at the central MDB CR's UID.

   t=10  Scale the central operator Deployment to 0 replicas (or helm
         uninstall it). No operator is running. mongods continue serving
         uninterrupted because the StatefulSets and Services are static.

   t=20  Apply the distributed-mode Helm chart on each member cluster.
         Three distributed operators boot. Each operator registers ONLY
         its own cluster as a runtime cluster — peer cluster names are
         known for FSM membership and deploymentState bookkeeping, but
         no cross-cluster Kubernetes API client is created.

   t=30  Replicate CRDs, the MongoDB CR, the project ConfigMap, the
         credentials Secret, and the operator member-list ConfigMap
         into each member cluster's namespace. StatefulSets already
         exist locally — they were created by hub-and-spoke at t=0.

   t=40  First reconcile on each operator:
         ├─ Gate 1 (resource-agreement): all three operators report
         │  identical hashes for the CR spec, the project ConfigMap,
         │  and the credentials Secret. Agreement holds.
         ├─ Each operator reads its local StatefulSets via the K8s
         │  API. The StatefulSet spec already matches what a from-
         │  scratch reconcile would produce.
         ├─ The scaler observes CurrentReplicas == DesiredReplicas
         │  and computes diff = empty.
         ├─ No StatefulSet write. No AutomationConfig publication.
         │  No pod restart.
         │
         └─ Gate 4 (leader): the merged AutomationConfig is already
            published to OM at the current generation. No AC publish
            is needed.

   t=300 Observation window closes. StatefulSet .status.currentRevision
         is unchanged across all nine StatefulSets. All mongod pod UIDs
         are unchanged. The OM AutomationConfig version is unchanged.

         ── Takeover invariant: ZERO disruption. ──
```

### Design implications discovered while building this

Implementing the takeover scenario surfaced four constraints on how distributed operators must be deployed, all of which are now part of the design.

**Membership configuration must be propagated to each member cluster.** The Helm chart's `multiCluster.clusters` value, and the `mongodb-kubernetes-operator-member-list` ConfigMap, must be present in each member cluster's namespace before the local operator starts. Without this, the operator sees only its own cluster as a member and treats the others as "down" — at which point it computes a sibling-cluster StatefulSet spec with `servers count = 0` and triggers a destructive reconcile. The membership data is what tells the operator "those other clusters exist and are running their own workloads; don't touch them".

**Distributed operators must not register Kubernetes clients for peer clusters.** The Helm chart's `multiCluster.clusters` value, in its default behaviour, mounts a kubeconfig Secret and triggers controller-runtime to register runtime clusters for every entry. In distributed pod-mode this is exactly wrong: there is no reachable Kubernetes API endpoint for peer clusters from inside a pod (the kubeconfig points at loopback addresses on the developer's host). The operator must record peer cluster names for FSM membership purposes but skip the runtime-client construction. The trigger for this distinction is the presence of the `RAFT_PEERS` environment variable.

**StatefulSet ownerReferences must be cluster-local in distributed mode.** In hub-and-spoke, member-cluster StatefulSets carry an ownerReference pointing at the central CR's UID. In distributed mode, each member cluster has its own MDB CR copy with its own server-assigned UID. If the existing hub-and-spoke StatefulSets still carry the central CR's UID after takeover, Kubernetes garbage collection on each member cluster sees the ownerRef as pointing to a UID that does not exist locally and reaps the StatefulSet. The fix is for distributed-mode StatefulSet writes to omit cross-cluster ownerReferences entirely; the operator handles its own lifecycle cleanup via label-driven `DeleteAllOf` on the standard resource-owner label.

**State must be rehydrated from live StatefulSets on the first reconcile after takeover.** A freshly-booted distributed operator has no persisted `deploymentState`, so its scaler initially sees `CurrentReplicas = 0` even though the local StatefulSet already has the target replica count. On the first reconcile in distributed mode (when `coordinator != nil` and `deploymentState.Status.ShardCount > 0`), the operator reads `Spec.Replicas` from the live local StatefulSets and populates the corresponding `Status.SizeStatusInClusters` slots before invoking the scaler. The scaler then sees `Current == Desired` and computes no diff.

---

## 12. Current status

The validation evidence for each top-level claim, summarised.

### Cross-cluster coordination without cross-cluster Kubernetes API access

Validated. The pod-mode end-to-end test suite for the multi-cluster sharded controller passes 6 of 6 tests on Evergreen. The test deploys the operator on each member cluster, creates a sharded MongoDB CR, waits for the cluster to reach Running, then exercises rolling restart, multi-member scale up, and multi-member scale down. Each step asserts a safety invariant measured against the actual replica-set quorum state.

### Cross-cluster ≤1 NotReady invariant during rolling restart

Validated. The safety monitor for `test_rolling_restart` reports a maximum of one voting member out-of-quorum at any moment across all clusters, for both `configSrv` and the shard. The measurement is taken from a combination of Kubernetes pod-lifecycle state (pod phase, deletion timestamp, container state, restart-count delta) and the replica set's own `rs.status()` member states. K8s pod-readiness is not used as a safety signal because it flickers during AutomationAgent reload events, producing spurious failures unrelated to actual quorum state.

### Cross-cluster ≤1 NotReady invariant during multi-member scale

Validated. With each cluster scaling its local shard StatefulSet by three voting members (one-at-a-time per reconcile step, serialised across clusters by the cross-cluster lease), the safety monitor reports a maximum of one out-of-quorum member at any moment.

### Leader-only OM writes with follower forwarding

Validated. Operator logs confirm that AutomationConfig publication is gated by the leader-only check and that follower clusters route their `Submit` calls to the leader's app-channel port. The leader address returned by `LeaderWithID` resolves correctly to the leader's FQDN.

### Hub-and-spoke unaffected by distributed code

Validated. A regression run of the hub-and-spoke multi-cluster sharded e2e on the same branch tip reaches the same state as a pre-distributed-PoC baseline. Every distributed-mode code path is gated by a `r.coordinator == nil` check; the legacy code path is byte-for-byte unchanged.

### Hub-and-spoke to distributed takeover with zero disruption

Partial. The snapshot-diff invariant (StatefulSet UIDs unchanged, currentRevision unchanged, pod UIDs unchanged, AutomationConfig version unchanged) has been demonstrated for the operator-swap event itself: when distributed operators boot against an existing hub-and-spoke deployment, they observe the cluster state, agree on the CR/CM/Secret hashes, and emit zero writes. The full end-to-end test that includes a post-swap functional check (apply a rolling-restart annotation, observe the distributed operators correctly roll the cluster) is currently blocked on a test-fixture infrastructure item — the `image-registries-secret` is not propagated from the central namespace to the member-cluster namespaces, causing the hub-and-spoke deploy phase of the test to fail before the distributed-takeover code is reached.

### Disaster recovery (one cluster lost, system continues)

Designed but not yet end-to-end tested. With a 3-cluster raft group, a single cluster loss leaves a majority quorum. The surviving operators continue to apply proposals and reconcile their local clusters. The lost cluster's `ComponentStatus` entries become stale and are detected by the Raft library's peer-contact timeout.

---

## 13. What was learned

The PoC produced six findings that materially shape how the design has to be implemented or deployed.

### 13.1 Dropping the Plan was a wrong simplification

The original proposal included an explicit Plan and Phase mechanism: the leader would construct a plan for each CR (a sequence of per-cluster steps to execute), and followers would execute steps the leader assigned them. During the design refinement before implementation, the Plan was dropped on YAGNI grounds and replaced with per-component leases — each operator decides what to do, then asks the FSM for permission via the lease.

In retrospect, the Plan was the cleaner primitive. The cross-cluster ordering invariants we needed to enforce ("only one cluster rolls at a time", "rolling restart waits for spec agreement", "scale up holds the lease until the local cluster reaches target") are naturally expressed as a leader-side workflow. Pushed into per-cluster races, the same invariants require several layers of additional state: lease keep-alive during gate waits, spec-generation invalidation of stale Ready bits, mongos-specific bypass of the cross-cluster mutex, and explicit deploymentState rehydration on takeover. Each of these is a layer on top of the lease primitive — none of them would exist if the leader simply decided "cluster N, do step S" and the follower executed it.

The current design works. But it suggests a v2 direction: keep the same Raft substrate and FSM tables, but replace the per-component lease with a leader-driven workflow. Section 14 sketches this.

### 13.2 The CR spec must be in the resource-agreement gate

The initial resource-agreement gate covered only the project ConfigMap and the credentials Secret. The MongoDB CR itself was not in the gate, on the assumption that the CR would be byte-identical across clusters because the test fixture replicates it explicitly.

This was wrong in two ways. First, in production GitOps deployments the CR can have transient skew between clusters during a push — one cluster's watch may fire on the new generation seconds before another cluster's. Second, even in the test fixture, the sequential nature of "write CR to cluster-1, then cluster-2, then cluster-3" produces a window in which operators have different views of the desired state.

The fix is to include a canonical JSON hash of the CR's `.spec` subtree in the agreed-resources set. Identical specs produce identical hashes regardless of per-cluster generation-counter drift or differences in `managedFields` records. The reconcile blocks until all operators have observed the same spec.

The hashing function matters: hashing the Go struct produces different hashes from hashing the wire JSON, because of decoder round-tripping and default-value defaulting. The implementation uses a canonical JSON serialisation of the spec subtree (sorted keys, recursive) before hashing.

### 13.3 Kubernetes pod-readiness is the wrong safety signal

The end-to-end tests' safety monitor initially used Kubernetes pod-readiness as a proxy for "this voting member is currently in quorum". During an `rs.reconfig` event — which fires every time the operator adds, removes, or changes a voting member — the AutomationAgent on every voting member reloads its AutomationConfig, and its readiness endpoint flickers to NotReady for a few seconds. The mongod process is still running and still serving; only the agent's health probe drops. But the test's safety monitor counts the NotReady samples and reports a cap-1 violation.

The fix is to measure quorum state directly: count voting members whose Kubernetes pod is in a lifecycle state inconsistent with "running mongod" (deletion timestamp set, container not Running, restart-count incremented), or whose `rs.status()` member state is not PRIMARY or SECONDARY. Both signals are sampled in parallel and unioned. The union catches both Kubernetes-side disruption (pod restarted, container terminated) and replication-side disruption (member partitioned, member in RECOVERING).

### 13.4 Stateless components need explicit exemption from the cross-cluster mutex

The original cross-cluster lease design treated all components uniformly. Applied to mongos, this produces a deadlock: cluster-1 acquires the `(CR, mongos)` lease, rolls its mongos pod, and never releases the lease because mongos has no `rs.reconfig` event to trigger MarkReady. The other clusters wait forever.

The architectural fact is that mongos does not have quorum semantics. There is no replica-set whose membership mongos participates in. Multiple mongos pods rolling in parallel is safe. The fix is a per-component `isCrossClusterMutexComponent` predicate; mongos returns false, the reconcile bypasses the lease entirely for mongos and proceeds directly.

Future stateless components (search nodes, for example) must explicitly opt out of the cross-cluster mutex in the same way.

### 13.5 Cross-cluster ownerReferences cause garbage-collection deletions on takeover

Hub-and-spoke writes member-cluster StatefulSets with `ownerReferences[0].uid` pointing at the central MDB CR's UID. This is intentional in hub-and-spoke — the central CR's deletion should cascade-delete the member StatefulSets via the standard Kubernetes garbage collector.

When distributed operators take over an existing hub-and-spoke deployment, the takeover protocol replicates the CR to each member cluster's namespace. Each replicated CR has a fresh server-assigned UID, different from the central CR's UID. The Kubernetes garbage collector on each member cluster then sees the existing StatefulSets carrying an ownerRef.UID that does not match any local CR UID and reaps the StatefulSets within seconds. The distributed operators see "no StatefulSets exist" and recreate them, producing exactly the workload disruption the takeover scenario was supposed to avoid.

The fix is to drop the ownerReferences entirely on StatefulSet writes when `coordinator != nil`. Distributed-mode StatefulSet lifecycle is owned by the local operator directly, via the existing label-driven `DeleteAllOf` cleanup in the OnDelete handler. Hub-and-spoke retains its ownerRef-driven cleanup unchanged.

### 13.6 Test fixtures and production deployments have different shapes

Several PoC validation cycles were spent on test-fixture problems that do not occur in production GitOps deployments. The takeover test's CR replication uses per-cluster CRD apply followed by a CR object creation, which produces fresh server-assigned UIDs (production GitOps would produce stable UIDs from the source YAML). The `image-registries-secret` is created only on the central kind cluster, not propagated to member-cluster namespaces (production: each cluster's secret management is independent). The `current.devc.kubeconfig` context can drift between test runs, breaking the test's CR-replication script (production: each operator only uses its own local kubeconfig).

These are not architectural issues, but they consumed enough validation time to be worth noting. A future PoC test harness should separate "set up a distributed deployment from scratch" from "simulate hub-and-spoke handoff" — they exercise different invariants.

---

## 14. Future direction: leader-driven workflow

The lessons in section 13.1 suggest a v2 direction. The Raft substrate, the FSM tables, the forwarder, the FQDN advertisement, the Istio passthrough configuration, the Helm chart wiring, and the hub-and-spoke compatibility model would all be preserved. What would change is the way per-component coordination is expressed.

### 14.1 Three event-driven reconcilers

Instead of one reconciler that combines decision-making with execution, the operator would register three controller-runtime reconcilers, each event-driven and each responsible for a narrow piece of work.

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

No background goroutine. No polling. Every reconcile is triggered by an event — either a Kubernetes watch event or a synthetic event from an FSM `Apply`. The follower's reconcile is a one-step operation: ask the leader for the next step, execute it, report the result.

### 14.2 Why this would be cleaner

The current design smears cross-cluster scheduling logic across several places: `distGateInline` in the controller, the lease table in the FSM, the `SpecGeneration` plumbing in the proposals, and the `isCrossClusterMutexComponent` predicate. With a leader-driven workflow, the scheduling logic lives in one place: a deterministic `Workflow.Advance` function that runs inside the FSM `Apply`. The follower's reconcile becomes "execute the step the leader gave me", which is naturally idempotent (executing the same StatefulSet apply twice is a no-op the second time).

Failover is simpler. On leader change, the new leader reads the workflow state from the FSM and resumes from the current step. There is no question of "is this stale" — the workflow state is the single source of truth and was applied through Raft.

The expected code-size delta: roughly 600 to 1000 lines removed (lease tables, `AcquireOrRespect`, `HasSiblingLease`, `IsComponentReady`, `MarkReady`, `distGateInline`, `distMarkReadyAndRelease`, `distReportInflightProgress`, `SpecGeneration` plumbing, `isCrossClusterMutexComponent`, scaler-rehydrate gates), 300 to 500 lines added (a `Workflow` type, `Workflow.Advance` function, step-kind dispatch, `GetMyNextStep`/`ReportStepResult` coordinator methods, FSM Apply for workflow proposals).

### 14.3 Migration story

Hub-and-spoke installations would remain unchanged. The transport code, the FSM scaffolding, and the Helm chart wiring all stay where they are. The distributed-mode path would swap under the same `if r.coordinator != nil` guard. Estimated effort: two to three weeks of focused engineering, with the same backwards-compatibility guarantees as the current design.

### 14.4 Why not now

The PoC has validated the architectural claim — that operators in different clusters can coordinate without cross-cluster Kubernetes API access while preserving replica-set safety invariants. Switching the implementation pattern mid-PoC would discard working code without producing additional evidence. The leader-driven workflow is the production-readiness step, informed by the lessons in section 13.

---

## 15. References

- `controllers/operator/mongodbshardedcluster_controller.go` — audit table at the top documenting the gate composition per write site.
- `pkg/coordination/raft/fsm_real.go` — `applyLeaseAllocate`, `HasSiblingLease`, FSM table definitions.
- `pkg/coordination/raft/transport_muxed.go` — raft listener with optional FQDN advertise address.
- `pkg/coordination/raft/forwarder.go` — follower → leader RPC channel implementation.
- `pkg/coordination/raft/production.go` — `BuildProductionCoordinator`, FQDN-from-`RAFT_PEERS` wiring.
- `controllers/operator/distributed_resource_agreement.go` — Gate 1 implementation: canonical-JSON CR hash, project CM hash, credentials Secret hash.
- `helm_chart/values.yaml` and `helm_chart/templates/operator.yaml` — `operator.distributed.enabled` block, Istio passthrough annotations, member-list ConfigMap.
- `docs/dev/distributed-multicluster-operator-implementation.md` — companion as-built notes.
- `docs/dev/phase-d-handoff.md` — development log.
