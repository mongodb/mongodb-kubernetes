# Distributed MongoDB Operator for Multi-Cluster Deployments

**Status:** Draft proposal
**Scope:** Architectural direction for replacing hub-and-spoke multi-cluster with a distributed, consensus-coordinated model.

---

## 1. Summary

Replace the current single-central-operator (hub-and-spoke) multi-cluster model with **one operator instance per cluster**, coordinated via **Raft consensus** between operators. Each operator reconciles only the slice of the deployment that lives in its own cluster. Cross-cluster ordering constraints (replica-set member-down rule, voting-member reconfig serialization, CA rotation, hostname changes) are expressed as a **plan with phases** persisted in the Raft state machine. Specs are content-hashed and agreed via Raft before any operator acts on them; CRs are synchronized to every cluster via GitOps as the durable substrate for disaster recovery.

The result:

- No cross-cluster Kubernetes API access required.
- No single point of failure — any cluster's loss is survivable.
- Automatic disaster recovery — surviving operators already hold the spec and replicated state machine.
- Compatible with a future direction in which Ops Manager (OM) is reduced to an optional integration rather than a hard runtime dependency.

---

## 2. Problem Statement

### 2.1 Current architecture (hub-and-spoke)

- A single central operator runs in a designated "hub" Kubernetes cluster.
- The hub holds a Secret containing a kubeconfig with credentials for every member cluster.
- The hub operator reads/writes resources in member clusters' Kubernetes API servers directly (StatefulSets, Services, Secrets, ConfigMaps, certificates).
- For replica sets, the hub publishes merged AutomationConfig to Ops Manager; OM's in-pod automation agents perform the per-pod rolling reconfig under MongoDB's "one voting-member change at a time" rule.
- **Cross-cluster StatefulSet rollout is explicitly serialized by the hub** (`controllers/operator/mongodbmultireplicaset_controller.go:568-572`): apply STS in cluster N, wait for `updated == ready == wanted`, then move to cluster N+1.

### 2.2 Pain points

1. **Single point of failure.** Loss of the hub cluster leaves the deployment unmanaged. Recovery requires reinstalling operator, CRDs, kubeconfigs, certs, and CRs in a new hub.
2. **Cross-cluster API connectivity.** Many customers refuse to open one cluster's Kubernetes API server to another cluster on security/compliance grounds.
3. **Manual disaster recovery.** No automated failover path exists today.
4. **OM SPOF.** OM is currently a hard dependency for both AutomationConfig publication and (transitively) for the runtime control plane. Future direction is to make OM optional.

---

## 3. Goals & Non-Goals

### Goals

- Eliminate cross-cluster Kubernetes API connectivity as a deployment requirement.
- Eliminate the central-cluster SPOF; any surviving cluster can continue managing the deployment.
- Preserve all existing replica-set safety invariants (member-down rule, voting-member-reconfig serialization).
- Provide a clean migration path that doesn't require operators in different modes to interoperate.
- Lay the foundation for OM-optional deployments (Phase 3+).

### Non-Goals

- Replacing Ops Manager as the source of truth for users, monitoring, or backups. Those remain OM concerns.
- Byzantine-fault-tolerant coordination. Raft assumes crash-stop, non-adversarial peers.
- Cross-cluster service-mesh design. Existing networking patterns (Envoy + SNI for search, hostnames in AC for replica sets) are preserved.
- Re-designing the CR schema beyond what's strictly required (some additive fields for explicit cluster lists may be needed).

---

## 4. Workload Types

Two distinct workloads must be supported. They share the per-cluster operator design and Raft substrate but differ in coordination requirements.

### 4.1 MongoDB Search (MongoT)

- Search nodes are read replicas of a MongoDB shard.
- MongoTs across clusters are **independent** — no cross-cluster ordering on restarts, no quorum semantics.
- Tolerates partition: clusters serve queries independently; resync on heal.
- **Coordination need:** agreement on the spec version (so all operators apply consistent configuration) and on Envoy peer hostnames (so each cluster's Envoy routes traffic correctly). No pod-restart serialization required.

### 4.2 MongoDB Replica Sets

- Members may be distributed across clusters for HA across DCs.
- Strict orchestration required:
  - MongoDB's `replSetReconfig` rejects more than one voting-member change at a time.
  - Pod restarts must be **globally serialized**: at most one mongod replica-set voting member may be in a non-running state at any moment across the entire deployment.
  - Scaling N → N+2 requires adding one member, waiting for full readiness + reconfig, then adding the next.
- **Coordination need:** full plan/phase machinery with cross-cluster restart authorization.

---

## 5. Architecture Overview

```
              ┌────────────────── Raft consensus ──────────────────┐
              │  State machine: agreed spec, plan, phase status,   │
              │  leader lease, per-cluster status, restart tokens  │
              └────────────────────────────────────────────────────┘
                       ▲              ▲              ▲
              mTLS gRPC│              │              │
                       │              │              │
            ┌──────────┴──┐    ┌──────┴──────┐    ┌──┴──────────┐
            │ Operator A  │    │ Operator B  │    │ Operator C  │
            │ (leader)    │    │ (follower)  │    │ (follower)  │
            └─────────────┘    └─────────────┘    └─────────────┘
                  │                  │                  │
       reconcile local│   reconcile local│   reconcile local│
       K8s objects   ▼   K8s objects   ▼   K8s objects   ▼
              ┌──────────┐       ┌──────────┐       ┌──────────┐
              │ Cluster  │       │ Cluster  │       │ Cluster  │
              │    A     │       │    B     │       │    C     │
              │ STS,Svc, │       │ STS,Svc, │       │ STS,Svc, │
              │ pods…    │       │ pods…    │       │ pods…    │
              └──────────┘       └──────────┘       └──────────┘
```

Three substrates carry information:

1. **GitOps** propagates the user-authored `MongoDB*` CR to every cluster. Each cluster ends up with an identical copy. This is the durable substrate that survives Raft quorum loss.
2. **Raft log** holds the runtime-authoritative state: agreed spec version (content-hashed), plan, phase progress, leader lease, per-cluster status, pod-restart tokens.
3. **Local Kubernetes API** is used only by each cluster's own operator to reconcile that cluster's resources. **No operator ever reads or writes a sibling cluster's Kubernetes API.**

---

## 6. Core Concepts

### 6.1 Per-cluster operator with cluster identity

Every operator pod is configured with its own `clusterName` at install time (Helm value). On reconcile, the operator:

1. Reads the local CR.
2. Filters `spec.clusterSpecList` to entries matching its own `clusterName`.
C: executes the reconcile logic similarly to what we have today, but the state machine decides which cluster should execute its part


3. Computes its **cluster index** (deterministic, agreed via Raft for replica sets; see §6.10).
4. Acts only on its local slice.

Resource naming is deterministic: `{crName}-{clusterIndex}`. Pods, Services, Secrets all use this scheme.
C: no change in resource naming, existing clusters get the same index as before

### 6.2 CR distribution via GitOps

The same CR (same `apiVersion`, `kind`, `metadata.name`, `spec`) is applied to every participating cluster. GitOps tooling (Flux, Argo, or equivalent) is the assumed mechanism. CRs are **not** propagated via Raft.

Rationale: a synchronized CR present in every cluster is required for disaster recovery. When a cluster is lost, surviving operators need the spec available locally so a new leader (in any surviving cluster) can act on it immediately, even if Raft has to be manually recovered first.

### 6.3 Content-hashed spec agreement

GitOps propagation is not atomic — clusters may receive a new CR generation tens of seconds apart. Operators must not act on a spec until every active operator agrees which version is current.

Mechanism:

1. When an operator's local CR changes, it computes `hash(spec)` and proposes `{specVersion: N+1, hash: H, content: …}` to Raft.
2. Raft commits when a quorum accepts the entry.
3. On commit, every operator's FSM holds the same canonical spec — including operators whose local CR copy hasn't been GitOps-synced yet (they receive the content via the Raft log).
4. Operators that subsequently observe a local CR with a *different* hash for the same generation refuse to execute and surface "out of sync with consensus" status. This catches misconfigured kustomize overlays, stale branches, or accidental drift.

This makes content-hash agreement the correctness boundary, not GitOps timing.

### 6.4 Plans and phases

The implicit "reconcile-and-requeue" state machine in today's controller becomes **explicit, persisted data** in the Raft FSM. A `Plan` is produced by the leader from `diff(currentAgreedSpec, previousAgreedSpec)` and committed as a single Raft entry:

```
plan {
  id: <uuid>
  generation: <specVersion>
  phases: [
    { id, action, target, waitFor, … },
    …
  ]
  currentPhase: <int>
  perClusterPhaseStatus: { A: "done", B: "pending", C: "pending" }
}
```

Followers replicate the plan. Each operator reads its slice of the current phase's action and executes locally. The leader advances `currentPhase` only when the `waitFor` condition evaluates true across the relevant scope (e.g., "all active clusters report `phaseStatus == done`").

### 6.5 Action vocabulary

Phases are composed from a finite, named vocabulary the operator understands:

| Action | Scope | Notes |
|---|---|---|
| `apply-stateful-set-template` | local | Idempotent. STS uses `OnDelete` strategy so this does not trigger pod restart. |
| `apply-service` | local | Variants for ClusterIP / NodePort / LoadBalancer / mesh modes. |
| `apply-trust-bundle` | local | Writes CA bundle to trust-store Secret. Agent picks up on next poll. |
| `rotate-leaf-cert` | per-pod | Local cert-manager re-issues; mount-path digest changes; pod becomes stale. |
| `delete-pod` | per-pod | Driver for actual restart. Only executed when a restart token is held. |
| `publish-automation-config` | leader-only | Writes merged AC. Today: to OM. Future: to operator-served local source. |
| `wait-agent-converged` | leader-only | Observation phase; no writes. |

New actions are added as new coordination needs emerge. The planner is the function that maps `diff(specs)` to a sequence of these actions plus their `waitFor` conditions.

### 6.6 Coordination patterns by change type

Different kinds of changes need different coordination signatures. The planner classifies the diff and emits an appropriate phase sequence.

| Class | Example | Affects peers? | Plan shape |
|---|---|---|---|
| **L** Local-only | adding a label to a local Service | no | single local `apply-service` phase |
| **R** Pod-restart | image bump, env-var change, resource bump | no (with OnDelete) | `apply-stateful-set-template` to all → per-pod `delete-pod` serialized globally |
| **B** Barrier | install new CA into trust store everywhere before rotating any leaf | yes, on prep | `apply-trust-bundle` (all clusters) with `waitFor: all-acknowledged` |
| **A** AC-coupled | LB hostname change must accompany AC update | yes | `apply-service` (target cluster) → `publish-automation-config` → `wait-agent-converged` → per-pod restart |
| **T** Multi-phase transaction | full CA rotation (dual-trust → rotate → drop old) | yes | multi-barrier plan with sequenced trust-bundle, leaf-rotation, and trust-bundle-cleanup phases |

### 6.7 Lifecycle controller (decoupling spec apply from pod restart)

A key invariant: **a StatefulSet template change must not, by itself, restart pods.** This is what makes barriered and AC-coupled plans safe.

All operator-managed StatefulSets use `updateStrategy.type: OnDelete`. Consequence:

- STS templates can be applied in every cluster in any order without triggering pod restarts.
- Pod restart happens only when the **lifecycle controller** explicitly deletes a pod.
- The lifecycle controller deletes a pod only when its cluster holds a restart token (see §6.8).

The lifecycle controller also enforces the safety floor: if Raft quorum is not present, it refuses to delete pods. Partitioned clusters automatically enter a safe-mode "no restarts" state.

### 6.8 Restart tokens

Pod restarts are the most safety-critical cross-cluster operation. Within a plan, a "restart" phase uses a per-pod **token** allocated by the leader:

```
restartToken { holder: clusterName, pod: podName, expiresAt: ts }
```

Flow:

1. Each operator computes its own stale pods (pods whose template hash differs from the current STS template) and reports them in its `clusterStatus`.
2. Leader allocates exactly one global token, choosing a stale pod that respects the global member-down rule.
3. The local operator for the holding cluster deletes the pod, waits for it to come back ready *and* for the automation agent's goal-state to be reached, then completes the token.
4. Leader picks the next stale pod (possibly in a different cluster) and reissues.

Token TTL handles operator failure mid-restart: if the holder vanishes, expiry returns the token to the leader for reissue.

### 6.9 Leader role

A single elected leader (Raft term holder) is responsible for:

- Proposing plan transitions: `diff → newPlan` and `advance currentPhase` writes.
- Publishing AutomationConfig to its destination (OM today; per-pod local source post-Phase-3, see §11).
- Allocating restart tokens.
- Advancing the `lastAchievedSpec` ±1 invariant (preserved from the current operator for replica set scaling).

Followers reconcile their local clusters from the replicated state but do not propose plan changes or publish AC. Failover is automatic: when the leader's cluster dies, Raft elects a new leader from survivors; the replicated FSM is intact, so the new leader resumes from `currentPhase` and `perClusterPhaseStatus` without state loss.

### 6.10 Cluster index assignment

Today the central operator assigns each member cluster a stable index used in StatefulSet naming. In the distributed model, assignment must be deterministic across all operators. Approach: the index map (`clusterName → index`) is part of the Raft-committed state. The first operator to observe a new cluster in the spec proposes an assignment; consensus fixes it. Once assigned, indexes are never reused even if a cluster is removed.

### 6.11 Status reporting

Each operator publishes per-reconcile a `clusterStatus` entry into Raft:

```
clusterStatus[clusterName] = {
  observedSpecGeneration: <int>
  observedSpecHash:       <string>
  observedSTSGeneration:  <int>
  stalePods:              [podNames…]
  readyPods:              [podNames…]
  lastReconcileError:     <string|null>
  agentGoalsReached:      [podNames…]
  trustBundleHash:        <string>
}
```

The leader reads this aggregated view and uses it to evaluate `waitFor` conditions. The user-facing CR `.status` is derived from the aggregated Raft state and mirrored back into the local CR copy in every cluster, so `kubectl get` shows consistent information regardless of which cluster the user queries.

---

## 7. Worked Examples

### 7.1 Image bump (7.0.1 → 7.0.2)

1. User edits CR. GitOps pushes to all clusters.
2. First operator to see new generation proposes `{specVersion: N+1, hash: H}` to Raft. Commits.
3. Leader produces plan:
   - Phase 1: `apply-stateful-set-template` (all clusters). Each operator applies new image to its local STS. `OnDelete` means no pod restarts.
   - Phase 2: `publish-automation-config` (leader). New goal version for agents.
   - Phase 3: per-pod `delete-pod` loop. Leader allocates `token{A, pod-0}`. A's operator deletes A/pod-0, waits for ready + agent goal-reached, completes token. Leader allocates next. Repeats across all 9 pods globally.
4. Plan complete. `lastAchievedSpec` advanced. CR status mirrored back.

### 7.2 LB switch in one cluster (B: ClusterIP → LoadBalancer)

1. Phase 1: `apply-service` target=B mode=loadbalancer. `waitFor`: B reports `svc.status.loadBalancer.ingress[].hostname` populated.
2. Phase 2: `publish-automation-config`. New hostname for B's processes goes into AC. `waitFor`: OM reports all agents goal-reached.
3. Phase 3: per-pod `delete-pod` for B's pods only. A and C are untouched.

### 7.3 CA rotation

1. Phase 1: `apply-trust-bundle` (all clusters), bundle = `old + new`. `waitFor`: all clusters report `trustBundleHash == hash(old+new)`. Existing connections still validate.
2. Phase 2: per-pod `rotate-leaf-cert + delete-pod` loop. Each pod's leaf is re-issued by its local cert-manager; mount-path digest changes; pod becomes stale; restart-token loop drains them.
3. Phase 3: `apply-trust-bundle` (all clusters), bundle = `new` only. `waitFor`: all clusters report `trustBundleHash == hash(new)`.

### 7.4 Replica set scale-up (3 → 5)

1. Phase 1: leader advances `lastAchievedSpec` by +1 (scale 3 → 4). `apply-stateful-set-template` in the relevant cluster grows by 1 replica. New pod starts.
2. Phase 2: `publish-automation-config` adds the new process.
3. Phase 3: `wait-agent-converged` until the new member is voting.
4. Plan complete. On next reconcile, leader produces a new plan for the next +1 step (4 → 5).

The ±1-per-cycle invariant is preserved exactly as in today's operator — it just moves from in-memory reconcile state to a Raft-committed `lastAchievedSpec` field.

---

## 8. Per-cluster Operator Reconcile Loop

```
reconcile():
  cr := readLocalCR()
  agreedSpec := raft.readAgreedSpec()

  # 1. Catch up consensus if the local CR is newer than what's agreed.
  if hash(cr.spec) != agreedSpec.hash and cr.generation > agreedSpec.generation:
    raft.propose(SpecUpdate{generation: cr.generation, hash: …, content: cr.spec})
    return  # next reconcile will see the committed value

  # 2. Idempotent local apply (never restarts pods).
  applyServices()
  applySecretsAndConfigMaps()
  applyStatefulSetTemplate()     # OnDelete strategy
  ensureLocalCerts()

  # 3. Publish observed status.
  raft.propose(StatusReport{cluster: myCluster, status: observe()})

  # 4. Leader-only.
  if raft.amLeader():
    runLeaderStep(agreedSpec)

  # 5. Token consumer (any operator).
  tok := raft.readToken()
  if tok != nil and tok.cluster == myCluster and not tok.expired():
    deletePod(tok.pod)
    waitUntil(podReady(tok.pod) and agentGoalReached(tok.pod))
    raft.propose(TokenComplete{id: tok.id})

runLeaderStep(agreedSpec):
  plan := raft.readPlan()
  if plan == nil or plan.generation < agreedSpec.generation:
    newPlan := planner.diff(previousAgreedSpec, agreedSpec)
    raft.propose(PlanCreate{plan: newPlan})
    return

  if plan.currentPhase.waitForSatisfied(raft.readAllStatus()):
    raft.propose(PlanAdvance{nextPhase: plan.currentPhase + 1})
    return

  if plan.currentPhase.needsRestartToken() and raft.readToken() == nil:
    candidate := pickNextStalePod(raft.readAllStatus())  # member-down-aware
    if candidate != nil:
      raft.propose(TokenAllocate{cluster: candidate.cluster, pod: candidate.pod})
```

`raft.propose(…)` redirects to the leader if called on a follower. Followers do not produce plan transitions; their proposals are limited to `SpecUpdate` and `StatusReport` (both idempotent and safe under contention — Raft serializes them through the log).

---

## 9. Networking and Trust

### 9.1 Raft transport

- HashiCorp's `raft.NetworkTransport` over TCP, wrapped with a custom `StreamLayer` providing mTLS via `tls.Dial`/`tls.Listen`.
- Each operator exposes a single port (e.g., 8443) via a Kubernetes Service. Typical exposure mechanisms: LoadBalancer Service per operator, or NodePort with stable DNS, or TCP/SNI-aware Ingress.
- Connection topology is mesh-among-peers, but in normal operation the leader maintains heartbeats with followers; followers don't talk to each other except during elections.

### 9.2 Trust

- A **shared CA certificate** is provisioned across all operators at deploy time.
- Each operator instance receives its own **mTLS client/server certificate** issued from that CA.
- Bootstrap is done by whatever automation deploys the operators (Helm chart inputs, SOPS-encrypted Git, sealed-secrets, cluster API integration with external KMS, etc.). No runtime trust negotiation.
- The CA used here can be separate from MongoDB's TLS CA. Mixing them is not recommended — different rotation cadences and trust audiences.

### 9.3 Peer discovery

- **v1:** Each operator is configured with the static list of peer endpoints at install time (`coordinator.peers: ["op-a.example.com:8443", …]`). Adding a cluster means updating the peer list everywhere and using `raft.AddVoter` to extend membership.
- **Future:** seed-based discovery via a small registry (e.g., a DNS SRV record, or a shared bootstrap endpoint) to make membership changes less manual. Out of scope for v1.

### 9.4 Service routing (MongoDB Search)

- An Envoy load balancer is deployed alongside MongoTs in each cluster.
- Envoy uses SNI on incoming connections to identify the target shard and route to the correct MongoT group.
- Each cluster's Envoy has a unique externally-routable hostname (encoding the cluster/DC identifier).
- Cross-cluster routing requires each operator to configure its local Envoy with knowledge of **all other clusters' Envoy hostnames**. This is why CRs must enumerate all participating clusters — even for search-only deployments where MongoTs don't otherwise need coordination.

---

## 10. Topologies

### 10.1 N ≥ 3 clusters

Standard Raft quorum. Tolerates floor(N/2) cluster failures. This is the recommended topology.

### 10.2 Two clusters (active + failover)

Native Raft can't form quorum with two voters. Two viable approaches:

- **Witness voter:** ship a small standalone binary that participates in Raft as a voter but does no reconciliation. Deployed in a third location (cloud VM, customer's bastion, an OM cluster). Standard "three voters, two workers" pattern. Treated as a first-class deployment shape, not a special case.
- **External etcd cluster** acting as the Raft log/coordination plane. Trades operator-embedded Raft for an external dependency.

Witness voter is preferred — keeps the protocol uniform (still Raft, still three voters) and avoids new infra dependencies.

### 10.3 Single cluster

Raft runs single-node: quorum is met by the one voter; commits succeed immediately. Same code path. No special-casing needed.

### 10.4 Manual reconfiguration after quorum loss

If a majority of clusters are permanently lost (e.g., regional disaster), Raft can't elect a leader. Recovery is **manual but supported**:

1. Admin determines which clusters are permanently gone.
2. On each surviving operator: stop the operator, run `raft.RecoverCluster` against the on-disk log/snapshot with a new `Configuration` listing only the survivors as voters, restart the operator.
3. Survivors form a new quorum and resume from the latest committed state.
4. New clusters can be added later via `AddVoter`.

This is exposed as an operator subcommand (e.g., `operator-cli raft recover --voters …`). It is intentionally not automatic — distinguishing "permanently lost" from "temporarily partitioned" is a human call.

---

## 11. Consensus Behavior and Partition Handling

### 11.1 Standard Raft semantics

- Entries commit when a majority quorum acknowledges them.
- Committed entries are durable across leader changes.
- If the leader fails after commit but before propagating to all followers, the next leader will have the entry (it came from the majority) and stragglers sync on reconnect.

### 11.2 Partition behavior

- Operators not in a quorum cannot propose plan transitions, allocate restart tokens, or publish AutomationConfig.
- The lifecycle controller refuses to delete pods when quorum is absent. Partitioned clusters self-block — no pod restarts means no risk of violating MongoDB constraints.
- Each operator continues reconciling local Kubernetes resources against its last-known-committed FSM state. Pods keep running, certificates keep renewing, services stay healthy. The control plane goes read-only; the data plane doesn't.

### 11.3 Workload survival during Raft quorum loss

Raft quorum is **independent of MongoDB replica set quorum**. While Raft is down:

- MongoDB itself continues serving (assuming its own replica set quorum is healthy).
- Existing pods stay up.
- No spec changes can be applied; no pods restart.
- Customers can tolerate "I can't change config for 30 minutes during a regional outage" much better than "my database goes down because the operator lost coordination."

This graceful-degradation property is the central availability argument for the design.

### 11.4 Quorum vs. unanimous

For pod-restart authorization specifically, majority quorum (Raft default) is sufficient. Partitioned clusters self-block via the lifecycle controller's quorum check — they cannot independently restart pods, so there's no risk that "majority decides X, minority decides not-X." Unanimous agreement would block plan progress on any single slow cluster and is rejected as a default.

---

## 12. CR Distribution and Disaster Recovery

GitOps-replicated CRs are a hard requirement, not a convenience.

- **Runtime authority:** Raft-committed `agreedSpec` (the FSM entry).
- **Durable substrate:** the CR present in every cluster.

The two co-exist because they serve different failure modes:

| Failure | Recovery |
|---|---|
| Leader cluster dies | Raft elects new leader from survivors; FSM intact; plan resumes from `currentPhase` |
| Raft quorum lost permanently | `raft.RecoverCluster` on survivors; survivors still have CRs locally (durable substrate); resume from last-committed FSM state |
| Fresh cluster added | New voter catches up via Raft log replication; GitOps applies the CR locally; operator joins reconcile loop |
| GitOps pushed inconsistent specs | Content-hash check at proposal time and at apply time surfaces the mismatch as a clear status error before any divergent action is taken |

A consequence: an admin can always answer "what is this deployment supposed to look like?" by reading any cluster's CR. This matters during incident response when the operator itself may be down or untrustworthy.

---

## 13. Library Choice: `hashicorp/raft`

Selected because it ships the network transport and durable-storage layers, leaving the FSM as the principal design surface.

### 13.1 What the library provides

- Raft algorithm (elections, log replication, commit, snapshotting).
- TCP-based transport (`raft.NetworkTransport`) with framing, retries, pipelining.
- Pluggable `StreamLayer` for TLS — we wire mTLS using our shared CA's cert pool.
- `raft-boltdb` for log and stable storage.
- `FileSnapshotStore` for snapshots.
- Membership change APIs (`AddVoter`, `AddNonvoter`, `RemoveServer`, `RecoverCluster`).

### 13.2 What we write

- **FSM** (`raft.FSM` interface): `Apply`, `Snapshot`, `Restore`. Mutations on the in-memory state machine based on decoded log entries.
- **Proposal types**: `SpecUpdate`, `StatusReport`, `PlanCreate`, `PlanAdvance`, `TokenAllocate`, `TokenComplete`, `ClusterIndexAssign`, plus an envelope for routing.
- **Leader-forwarding shim**: when a non-leader's reconcile calls `propose`, redirect via the standard "current leader address" lookup.
- **StreamLayer**: thin wrapper over `tls.Dial`/`tls.Listen` with our cert pool.
- **Bootstrap and recovery CLI**: idempotent first-boot bootstrap, plus `raft recover` admin subcommand.
- **Storage adapter (optional)**: an alternative `LogStore`/`StableStore` backed by ConfigMaps if PV provisioning is not desired (see §13.3).

### 13.3 Durable storage choice

The operator runs without a persistent volume by default in the existing deployment. Two options for Raft log persistence:

- **PV-backed `raft-boltdb`** (standard). Requires provisioning a PV for the operator. Simplest, well-tested.
- **ConfigMap-backed `LogStore`** (custom). Writes log entries to a ConfigMap (or set of rotating ConfigMaps). Avoids PV provisioning at the cost of a custom storage layer and higher write latency.

Write frequency expectation: log writes correspond to spec changes, plan advancement, status reports, and token transitions — all on the order of seconds-to-minutes during active operations, idle otherwise. Raft heartbeats are network-only and do not produce log entries. ConfigMap-backed storage is plausible if benchmarking confirms acceptable latency on real Kubernetes clusters under load. PV-backed is the safe default.

Recommendation: start with PV-backed for v1, evaluate ConfigMap-backed once core protocol is stable.

---

## 14. OM Integration Trajectory

Today, the operator publishes merged AutomationConfig to OM and OM's in-pod agents execute rollouts. The distributed-operator design preserves this end-to-end in v1 — the only change is *which* operator publishes to OM (the leader, instead of the hub).

Strategic direction (Phase 3+) is to reduce OM as a hard runtime dependency:

| Phase | OM role |
|---|---|
| 1, 2 | OM remains AutomationConfig source-of-truth (unchanged from today) |
| 3 | Per-pod ConfigMap mirrors the AC content; agent reads locally; OM is still updated for observability but is no longer on the runtime path |
| 4 | OM is fully optional; the operator's Raft FSM is the canonical AC source; agent reads from per-pod local ConfigMap maintained by the local operator |

This separation matters: the coordination layer (Raft-among-operators) and the agent-config-source layer (currently OM, eventually operator) are distinct concerns. The distributed-operator work delivers the coordination layer; the OM-decoupling work is downstream.

Out of scope for this proposal: the agent-side contract changes required for Phase 3/4. Those depend on a separate spike on the automation agent.

---

## 15. Implementation Strategy

A workload-driven phasing (Search first, Replica Set second) layered on an architectural sub-progression.

### 15.1 Phase 1 — MongoDB Search (MongoT) multi-cluster

Simpler scope: search nodes are independent across clusters, so no pod-restart coordination is needed. But the per-cluster operator model, mTLS connectivity, and CR-version agreement are all required.

Deliverables:

1. Per-cluster operator deployment model (replacing hub-and-spoke for search).
2. mTLS endpoint between operators (transport + cert plumbing).
3. CR schema with explicit list of all participating clusters and their Envoy hostnames.
4. **Raft cluster** between operators, with a minimal FSM (agreed spec version + per-cluster status). Even though search doesn't need restart serialization, using Raft from day one avoids re-implementing CR-version polling logic that's thrown away in Phase 2.
5. Envoy configuration logic that consumes the multi-cluster CR (each cluster knows all peer hostnames).
6. GitOps-friendly deployment of identical CRs.

Alternative considered: a lightweight CR-version polling protocol instead of Raft for Phase 1. Rejected because (a) the marginal cost of Raft for a small FSM is low, (b) Phase 2 needs Raft anyway, and (c) maintaining two coordination protocols (one for search, one for replica sets) is harder than one. The same code path covers both.

### 15.2 Phase 2 — MongoDB Replica Set multi-cluster

Adds plan/phase machinery and restart coordination on top of Phase 1's foundation.

Deliverables:

1. **Planner**: `diff(prevAgreed, newAgreed) → Plan`. Handles all change classes from §6.6.
2. **Plan/phase state in FSM**: plan creation, phase advancement, completion criteria evaluation.
3. **Lifecycle controller**: per-pod restart driver, `OnDelete` strategy enforcement, token consumption.
4. **Restart token allocation**: leader-side logic respecting global member-down rule.
5. **Cluster index assignment via Raft**.
6. **Unified CR status mirroring**: leader writes aggregated status to its local CR's `.status`; followers mirror.
7. **`lastAchievedSpec` ±1 invariant**: moved from in-memory operator state to Raft FSM.
8. **Witness voter binary**: lightweight standalone Raft voter for 2-cluster topologies.
9. **`operator-cli raft recover` subcommand**: documented DR procedure.

### 15.3 Phase 3+ — OM decoupling (out of scope here)

Spike on automation-agent integration; design doc separate. Not gated by Phase 1/2.

### 15.4 Migration from hub-and-spoke

Existing hub-and-spoke deployments need an upgrade path. Recommended approach:

1. Install operators in member clusters in "follower-candidate" mode (joins Raft as non-voter, observes only).
2. Convert hub operator to Raft leader; replicate state into Raft.
3. Promote member operators to voters one at a time. Hub-and-spoke kubeconfig usage is drained.
4. Switch StatefulSets to `OnDelete` (one-time per-STS change; can be done in place with `--cascade=orphan`).
5. Hub operator becomes a normal peer; nothing special about its cluster afterwards.

This is the most operationally complex part of the work and warrants its own runbook.

---

## 16. Summary of What Needs to Be Built

### Phase 1 (Search)

- [ ] Per-cluster operator deployment model.
- [ ] mTLS transport (CA + cert provisioning at install time).
- [ ] Static peer discovery via Helm values.
- [ ] CR schema with explicit cluster list + Envoy hostnames.
- [ ] Raft cluster bring-up with minimal FSM (agreed spec + status).
- [ ] Content-hashed `SpecUpdate` proposals.
- [ ] Envoy config generation consuming multi-cluster CR.
- [ ] GitOps reference deployment.

### Phase 2 (Replica Set)

- [ ] Full FSM (plan, phases, tokens, cluster indexes, leader lease, lastAchievedSpec).
- [ ] Planner (action vocabulary + change-class diff → plan).
- [ ] Lifecycle controller with `OnDelete` strategy.
- [ ] Restart-token allocation respecting member-down rule.
- [ ] Per-cluster status reporting + leader-side aggregation.
- [ ] Unified CR `.status` mirroring.
- [ ] Witness voter binary for 2-cluster topologies.
- [ ] `operator-cli raft recover` subcommand.
- [ ] Migration tooling from hub-and-spoke.
- [ ] CR status mirroring across clusters.

---

## 17. Open Questions

- **Storage backend.** Start with PV-backed `raft-boltdb` or invest in ConfigMap-backed `LogStore` from day one? Recommendation: PV-backed for v1; benchmark ConfigMap-backed before committing.
- **Witness voter packaging.** Standalone binary? Container image? Helm chart? Where does the customer run it (cloud VM, OM cluster, customer's existing bastion)?
- **Membership changes during partition.** If a cluster is unreachable for an extended period, when do we demote it from voter? Manual via `RemoveServer`? Automatic after a threshold? The latter risks split-brain-via-flapping.
- **Operator-to-operator port exposure.** LoadBalancer Service per operator is the simplest path but adds N public/private IPs. Service mesh integration could be cleaner but is heavier. Customer feedback needed.
- **Phase 1 Raft commitment.** Does using Raft from day one for search create surprises that polling would have hidden? Worth a small spike before committing.
- **Migration path UX.** The hub-and-spoke → distributed transition has several moving parts (operator installs, voter promotion, STS strategy flip, kubeconfig drain). Runbook needs piloting on a real customer cluster.
- **CR `.status` write contention.** If every cluster mirrors aggregated status into its local CR, that's N writers to N independent CR copies. Each is local-only so they don't conflict, but inconsistent timing means `kubectl get` in different clusters can show slightly different status snapshots. Acceptable? Or do we want only the leader to write status?
- **Agent-side contract for Phase 3.** Out of scope here but blocks the OM-decoupling endgame. Needs its own design.

---

## 18. References

- Current hub-spoke serialization: `controllers/operator/mongodbmultireplicaset_controller.go:568-572` (cross-cluster STS apply guard).
- Cluster index assignment today: `api/v1/mdbmulti/mongodb_multi_types.go:609-630` (`ClusterNum()`).
- StatefulSet ready check today: `pkg/kube/statefulset/inspect/statefulset_inspector.go:50-55` (`IsReady`).
- AutomationConfig publication today: `controllers/operator/mongodbmultireplicaset_controller.go:766-771` (`ReadUpdateDeployment`).
- HashiCorp Raft: <https://github.com/hashicorp/raft>
- Raft paper: Ongaro & Ousterhout, "In Search of an Understandable Consensus Algorithm" (2014).

---

## 19. Appendix A: Mapping the Sharded Cluster Reconciler

This appendix maps the existing `controllers/operator/mongodbshardedcluster_controller.go` reconcile flow onto the new-model coordination categories: **leader-only**, **lease-gated** (cluster gets a lease, executes locally, signals completion via Raft), **local-only** (each operator independently against its own cluster), and **decommissioned** (cross-cluster patterns that don't exist in the new model).

The reference implementation for the new model is intentionally chosen as the sharded cluster reconciler because it exercises every coordination axis the proposal needs to handle: shards, config servers, mongos, multi-component ordering, per-cluster STS serialization, AC publication, and complex state persistence.

### 19.1 Phase-by-phase mapping

Entry: `ShardedClusterReconcileHelper.Reconcile()` at `controllers/operator/mongodbshardedcluster_controller.go:849`.

| # | Today's phase | File:line | Today's behaviour | New-model treatment |
|---|---|---|---|---|
| 1 | Validation | 851 (`ProcessValidationsOnReconcile`) | Per-reconcile spec validation | **Local-only.** Every operator validates against the Raft-agreed spec. Disagreement surfaces in `clusterStatus`. |
| 2 | Search resource binding | 863–866 (`applySearchParametersForShards` 1028–1080) | Look up linked MongoDBSearch CR; inject search-related `setParameter` options (`mongotHost`, `searchIndexManagementHostAndPort`, `useGrpcForSearch`, `searchTLSMode`) into `r.desiredShardsConfiguration[*].AdditionalMongodConfig` and `r.desiredMongosConfiguration.AdditionalMongodConfig`. These values flow into AC process definitions (used by `createDesiredMongosProcesses` 2357 and shard process builders). | **Leader-only.** This is a preparation step for AC publication, not a K8s-resource step. Leader reads the (GitOps-replicated) MongoDBSearch CR locally, folds search params into the AC it publishes. Followers don't run this — they never publish AC. Note: followers *do* read the MongoDBSearch CR for unrelated purposes (local Envoy peer config, local MongoT StatefulSet management), but those are handled by the search controller (`controllers/searchcontroller/mongodbsearch_reconcile_helper.go`), not by this phase. |
| 3 | Scaling direction check | 872–875 | Block conflicting scale-up + scale-down | **Leader-only.** Leader's planner enforces this; followers don't propose conflicting transitions. |
| 4 | Cluster spec removal guard | 881–883 | Prevent removing a non-empty cluster | **Leader-only.** Global decision encoded as planner refusal. |
| 5a | Agent key replication | `replicateAgentKeySecret` 2658 | Read agent key centrally, write to every member's Secret | **Decommissioned cross-cluster write.** Leader writes agent key (encrypted entry) into Raft FSM. Each local operator reads from FSM and writes its own Secret. |
| 5b | Hostname override ConfigMap | `reconcileHostnameOverrideConfigMap` 2716 | Build hostnames centrally, write to every member's CM | **Local computation.** Hostname map is derivable from the spec (identical everywhere via Raft). Each operator builds the same map and writes its own ConfigMap. |
| 5c | SSL/MMS CA ConfigMap | `replicateSSLMMSCAConfigMap` 2968 | Read CA centrally, write to every member | **Replace with Raft-shared CA.** Leader publishes CA bundle to FSM; locals materialise it. |
| 6a | X.509 ensure | `ensureX509SecretAndCheckTLSType` 1189 | Per-cluster local | **Local-only — unchanged.** |
| 6b | STS create/update (config srv, shards, mongos) | `createOrUpdateConfigServers` 1563, `createOrUpdateShards` 1510, `createOrUpdateMongos` 1473 | Per-component, per-cluster loop with per-cluster blocking when not first scaling (1585–1589, 1543–1547, 1491–1495) | **Lease-gated.** Leader allocates a lease scoped `{component, shardIdx, clusterName}`. The cluster holding the lease executes the existing `CreateOrUpdate` + `GetStatefulSetStatus` against its local client, then proposes lease-complete. Leader picks next. |
| 6c | AutomationConfig publication | `updateOmDeploymentShardedCluster` 1985 → `publishDeployment` 2069 | Gather processes across all shards/clusters, two-stage merge, `ReadUpdateDeployment`, `WaitForReadyState` | **Leader-only.** Leader has the agreed spec + aggregated per-cluster status from FSM. Builds processes via `createDesiredShardProcessesAndMemberOptions` 2393 / `createDesiredConfigSrvProcessesAndMemberOptions` 2376 / `createDesiredMongosProcesses` 2352 (replacing "first healthy cluster" sourcing at 2074/2085 with FSM spec read). Calls `ReadUpdateDeployment` from leader's cluster only. |
| 6d | Wait-for-agent-register | `waitForAgentsToRegister` 1986 | Block until expected agents ping OM | **Leader-only**, becomes a phase with `waitFor: all-agents-registered`. |
| 6e | WaitForReadyState | 2016, 2040, 2209 | Poll OM until processes reach goal | **Leader-only**, same shape. |
| 7 | Scaling step control (±1) | `shouldContinueScalingOneByOne` 943 | Return `Pending()` to continue scale steps | **Leader-only.** ±1 invariant moves from `deploymentState.Status.MongodbShardedClusterSizeConfig` to Raft-committed `lastAchievedSizeConfig`. Leader produces successive plans, one step at a time. |
| 8 | Scale-down STS cleanup | `removeUnusedStatefulsets` 956 | Delete drained STSes per cluster | **Lease-gated**, same pattern as 6b. |
| 9 | State persistence | `StateStore` ConfigMap write 2587, `LastAchievedSpec` 992 | Annotate CR / write ConfigMap | **Decommissioned in its current shape.** `ShardedClusterDeploymentState` moves into Raft FSM. Each operator may mirror it to a local ConfigMap and/or CR `.status` for `kubectl` visibility. |

### 19.2 The cluster-iteration pattern, transformed

The repeated structure at `mongodbshardedcluster_controller.go:1583–1598` (and its three near-identical copies for shards, mongos, config servers) is the load-bearing thing we externalise. Today it's:

```go
for _, cluster := range healthyMemberClusters {
    sts := buildSts(cluster)
    CreateOrUpdate(cluster.client, sts)
    status := GetStatefulSetStatus(cluster.client, sts)
    if !ScalingFirstTime && !status.IsOK() {
        return status   // serialize: don't touch next cluster
    }
}
```

In the new model, this body is preserved — only the surrounding control flow moves to Raft. Every operator runs:

```go
// Every operator runs this on every reconcile.
if !raft.imHoldingLease({component, shardIdx, myCluster}) {
    return waitForLease
}
sts := buildSts(myCluster)
CreateOrUpdate(localClient, sts)                              // local API only
status := GetStatefulSetStatus(localClient, sts)
raft.propose(StatusReport{stsGen: ..., ready: ..., stalePods: ...})
if status.IsOK() {
    raft.propose(LeaseComplete{component, shardIdx, myCluster})
}
```

And on the leader:

```go
phase := plan.currentPhase
if phase.action == "apply-statefulset" && raft.activeLease == nil {
    next := pickNextClusterForPhase(phase, perClusterStatus)
    raft.propose(LeaseAllocate{phase, next})
}
if phase.action == "apply-statefulset" && allLeasesComplete(phase) {
    raft.propose(PlanAdvance{nextPhase: phase + 1})
}
```

The functions inside the lease block — `buildSts`, `CreateOrUpdate`, `GetStatefulSetStatus`, the IsOK check — remain unchanged from today. The for-loop over clusters is replaced by a "do my slice when leased" gate, and the wait-then-continue logic moves from operator reconcile-and-requeue state to the leader's plan-advance step.

### 19.3 State migration

Specific operator-memory / ConfigMap fields move to Raft FSM:

| Today | Raft FSM field |
|---|---|
| `ShardedClusterDeploymentState.LastAchievedSpec` | `agreedSpec` |
| `ShardedClusterDeploymentState.Status.MongodbShardedClusterSizeConfig` | `lastAchievedSizeConfig` |
| `ShardedClusterDeploymentState.Status.SizeStatusInClusters` | `clusterStatus[name].observedSizes` |
| `ShardedClusterDeploymentState.LastConfiguredRoles` | `lastConfiguredRoles` |
| `ScalingFirstTime` / `ReplicasThisReconciliation` per-component scalers | derived in-FSM from `lastAchievedSizeConfig` + `agreedSpec` |
| `StateStore` ConfigMap | gone — replaced by FSM. Optional local mirror for visibility. |

The migration of `MongodbShardedClusterSizeConfig` and `SizeStatusInClusters` is particularly clean because the existing logic already treats them as a per-cluster keyed map — exactly the shape of the new `clusterStatus[…]` structure.

### 19.4 Reusable vs. decommissioned code

**Reused unchanged (called by lease holder or by leader, against local client / OM):**

- `buildSts` / `buildConfigSrvSts` / `buildMongosSts`
- `createDesiredShardProcessesAndMemberOptions` / `createDesiredConfigSrvProcessesAndMemberOptions` / `createDesiredMongosProcesses`
- `publishDeployment` two-stage merge
- `WaitForReadyState`, `CalculateDiffAndStopMonitoring`
- `ensureX509SecretAndCheckTLSType`
- `GetStatefulSetStatus`, `IsReady`

**Decommissioned:**

- `replicateAgentKeySecret` (2658)
- `reconcileHostnameOverrideConfigMap` (2716)
- `replicateSSLMMSCAConfigMap` (2968)
- The cross-cluster `for _, cluster := range healthyMemberClusters` orchestration loops in the three component update functions (1479, 1517, 1569)
- `StateStore` ConfigMap-backed persistence (777–820, 2587)
- `memberClusterClientsMap` and the kubeconfig-loading machinery in `pkg/multicluster/multicluster.go` (the entire cross-cluster client dispatch layer)

### 19.5 Raft FSM proposal vocabulary for sharded clusters

Proposals the sharded cluster reconciler needs to submit:

| Proposal | Issuer | Effect |
|---|---|---|
| `SpecUpdate{generation, hash, content}` | any operator on local CR change | Commit canonical spec |
| `StatusReport{cluster, status}` | every operator each reconcile | Update `clusterStatus[cluster]` |
| `ClusterIndexAssign{cluster, index}` | leader on first observation | Fix index assignment |
| `PlanCreate{plan}` | leader after diff | Persist new plan |
| `PlanAdvance{nextPhase}` | leader after `waitFor` satisfied | Move to next phase |
| `LeaseAllocate{phase, cluster}` | leader during lease-gated phase | Authorise a cluster's slice |
| `LeaseComplete{phase, cluster}` | lease holder on success | Release lease, signal phase progress |
| `SizeConfigAdvance{newConfig}` | leader after a ±1 step succeeds | Persist `lastAchievedSizeConfig` |
| `RolesUpdate{newRoles}` | leader after RBAC applied | Persist `lastConfiguredRoles` |
| `CASecretUpdate{caBundle}` | leader on CA rotation | Distribute CA via FSM |
| `AgentKeyUpdate{key}` | leader after OM agent-key creation | Distribute agent key (encrypted entry) via FSM |

All proposals are idempotent under retry — important for Raft safety, and the existing reconciler's idempotent CreateOrUpdate semantics make this natural.

### 19.6 Open implementation questions specific to sharded clusters

- **Per-shard lease granularity vs. per-component.** Is a single lease "do all shards in cluster X" sufficient, or does each shard need its own lease? Today the inner loop iterates one shard at a time per cluster; the natural lease grain is `{shardIdx, clusterName}`. The leader can choose to interleave shards or drain by cluster. This is a planner-design call.
- **Component ordering.** Today the order is config servers → shards → mongos (1457–1467), with downgrade exception. The planner must preserve this; encode it as plan phases (`phase 1: config servers`, `phase 2: shards`, `phase 3: mongos`) so it's explicit and inspectable.
- **Recovery-mode behaviour.** Today the operator has a "automatic recovery" path (1214–1224) that runs AC and K8s reconciliation serially when the deployment is stuck. In the new model, recovery becomes the leader force-creating a fresh plan from `(currentAgreedSpec, observedState)` — same intent, persisted as a Raft entry rather than ad-hoc reconcile logic.
- **Encrypting sensitive FSM entries.** Agent key and (if used) cert private keys in the Raft log require encryption at rest. Options: encrypt those specific proposal types with an operator-managed key (key itself bootstrapped at install time, never written to Raft); or rely on BoltDB-level disk encryption from the underlying volume. The first is cleaner because it doesn't depend on storage substrate.

---

## 20. Appendix B: Minimal PoC Plan

### 20.1 Goal

Prove end-to-end that a distributed per-cluster operator coordinated via Raft can deploy and manage a **multi-cluster sharded MongoDB cluster** without cross-cluster Kubernetes API access. The PoC validates the protocol shape, not production-grade durability or migration.

Load-bearing claims the PoC must demonstrate:

1. Three operators in three K8s clusters can form a Raft cluster over mTLS.
2. The same sharded cluster CR applied to all three clusters is reconciled into a coherent agreed spec via Raft.
3. STS create/update is **lease-gated**: leader allocates one cluster a lease at a time, that operator writes locally only, signals completion via Raft, leader allocates next.
4. AutomationConfig publication is **leader-only**: exactly one operator (the Raft leader) publishes to Ops Manager; followers never call OM-write APIs.
5. The deployed sharded cluster is operationally healthy (mongos routes queries; shards have data; config servers maintain metadata).
6. **Disaster recovery drill**: kill the leader's pod; a new leader is elected; an in-flight plan resumes correctly.

### 20.2 Scope

**In scope:**

- 3 K8s clusters (kind, local). Min quorum, simplest topology.
- One sharded cluster CR: 1 shard, 3 mongod replicas, 1 config server (3 replicas), 1 mongos. Distributed across the 3 clusters (one replica per cluster per component).
- emptyDir-backed Raft log (per prior decision).
- mTLS between operators using a shared bootstrap CA.
- Static peer configuration via Helm values / env vars.
- Sealed entries for the agent key (validates the encryption-at-FSM-level design).
- OM integration unchanged (leader publishes AC to OM; agents pull from OM).
- CRs applied via `kubectl apply` to each cluster manually (GitOps is the production path; for PoC, manual apply simulates it).

**Out of scope:**

- TLS for MongoDB itself (skip cert-manager; use no-TLS for the PoC mongod). Operator-to-operator mTLS *is* in scope.
- Cert rotation flows.
- OnDelete StatefulSet strategy and per-pod restart tokens. PoC uses default RollingUpdate; OM agent gating provides intra-cluster serialization, and cross-cluster STS serialization is what we're actually proving via leases.
- Full change-class taxonomy. PoC handles `SpecUpdate` and one lease-gated STS phase. No barriers, no AC-coupled multi-phase.
- CR status mirroring back to local CRs.
- Witness voter (2-cluster topology). 3 clusters only.
- Migration from hub-spoke.
- Search workload.
- ConfigMap-/Secret-backed LogStore. Disk via emptyDir is the PoC choice.
- Production-grade error handling. Best-effort + clear logs is sufficient.

### 20.3 Topology

```
  ┌───────────────────────────────────────────────────────────────┐
  │  Local dev machine (or single CI runner)                      │
  │                                                                │
  │  ┌─ kind cluster A ─┐  ┌─ kind cluster B ─┐  ┌─ kind cluster C ┐│
  │  │ operator-A       │  │ operator-B       │  │ operator-C      ││
  │  │ (Raft node a)    │  │ (Raft node b)    │  │ (Raft node c)   ││
  │  │ + mongod-shard-0 │  │ + mongod-shard-1 │  │ + mongod-shard-2││
  │  │ + mongod-cfg-0   │  │ + mongod-cfg-1   │  │ + mongod-cfg-2  ││
  │  │ + mongos-0       │  │ (no mongos)      │  │ (no mongos)     ││
  │  └──────────────────┘  └──────────────────┘  └─────────────────┘│
  │           │                     │                     │         │
  │           └─── Raft mTLS over ──┴──── host-network ───┘         │
  │                                                                  │
  │  Ops Manager (existing dev OM, accessible from all 3 clusters)  │
  └───────────────────────────────────────────────────────────────┘
```

Peer reachability across kind clusters: docker bridge or kind's built-in inter-cluster networking. Each operator exposes its Raft port via `NodePort` Service; peer addresses are `host.docker.internal:<nodePort>` or equivalent.

### 20.4 Components to build

| # | Path | Purpose | LOC estimate (rough) |
|---|---|---|---|
| 1 | `pkg/coordination/raft/manager.go` | Embed hashicorp/raft, lifecycle (bootstrap/join/shutdown), peer config from env | 200 |
| 2 | `pkg/coordination/raft/transport.go` | `StreamLayer` wrapping `tls.Dial`/`tls.Listen` with shared CA | 100 |
| 3 | `pkg/coordination/raft/fsm.go` | FSM with `Apply`, `Snapshot`, `Restore`. State: agreedSpec, plan, perClusterStatus, activeLease, sealedAgentKey | 300 |
| 4 | `pkg/coordination/raft/proposals.go` | Proposal types: `SpecUpdate`, `StatusReport`, `PlanCreate`, `PlanAdvance`, `LeaseAllocate`, `LeaseComplete`, `AgentKeyUpdate` | 150 |
| 5 | `pkg/coordination/raft/seal.go` | Sealed-entry encrypt/decrypt with `keyId` envelope (PoC uses single hardcoded key, no rotation) | 80 |
| 6 | `pkg/coordination/raft/leader.go` | Leader-side logic: diff→plan, advance phases, allocate leases | 200 |
| 7 | `controllers/operator/distributed_sharded_reconciler.go` | New reconciler entry that wraps the existing `ShardedClusterReconcileHelper`, gates writes on lease, filters spec to local cluster, skips cross-cluster replication functions | 400 |
| 8 | `main.go` (modified) | New `--mode=distributed` flag; reads `--cluster-name`, `--peer-addresses`, mTLS cert paths | 50 |
| 9 | `helm_chart/values-distributed.yaml` + templates | New Helm values for distributed mode (peer list, cluster name, mTLS Secret refs, NodePort for Raft) | 150 |
| 10 | `scripts/poc/setup-kind-3cluster.sh` | Bring up 3 kind clusters, install operators, configure peer connectivity, pre-provision CA + mTLS certs | 200 |
| 11 | `scripts/poc/smoke-test.sh` | Apply CR, wait for sharded cluster ready, run `mongosh` query to validate | 100 |
| 12 | `scripts/poc/dr-drill.sh` | Kill leader pod, wait for new leader, apply a CR change, validate it propagates | 100 |

Total new code: ~2000 LOC. Most of it (item 7) is glue over existing reconciler functions.

### 20.5 Milestones

Each milestone is independently demoable.

#### M0 — Raft skeleton (de-risk the unknown)

**Goal:** Three pods, three kind clusters, hashicorp/raft running over mTLS, leader elected, `raft.Apply` works.

**Deliverables:** items 1, 2, 10. No operator logic yet — just a standalone test binary that spins up Raft and exposes a debug HTTP endpoint to apply test entries.

**Acceptance:**
- `kubectl logs operator-A` shows "Raft leader: cluster-A" (or whichever).
- `curl operator-A:8080/apply -d 'hello'` returns success; `curl operator-B:8080/state` shows the entry.
- Killing the leader pod causes a new election within 5s; new leader picks up.

#### M1 — Spec agreement

**Goal:** Operator reads its local CR, proposes `SpecUpdate`, all three operators agree on the canonical spec via Raft.

**Deliverables:** items 3, 4, 5, plus minimal `main.go` integration (item 8).

**Acceptance:**
- Apply the same sharded cluster CR to all 3 kind clusters.
- All three operators log: "Agreed spec hash = …, generation = 1."
- Apply a slightly different CR to one cluster (simulating GitOps drift). That operator logs "Local CR hash mismatch with consensus; refusing to act."

No K8s resources are reconciled yet — just spec agreement.

#### M2 — Lease-gated STS apply

**Goal:** Leader produces a minimal plan; each operator waits for its lease, applies its local StatefulSets (shard + config server + mongos as appropriate to its cluster's slice), signals completion. Cross-cluster serialization visible in logs.

**Deliverables:** item 6, partial item 7 (just the STS-apply path, no AC publication yet).

**Acceptance:**
- After M1's spec agreement, leader logs "Plan generation 1 created: phase 1 = apply-sts (shard-0) to cluster-A."
- cluster-A's operator logs "Holding lease for {shard-0, cluster-A}; applying STS."
- cluster-A's operator: `kubectl get sts -n mongodb` shows the new STS.
- cluster-A operator logs "Lease complete." Leader logs "Phase advance: apply-sts (shard-1) to cluster-B."
- Sequence repeats for B, then C, then config servers, then mongos. No STS appears in cluster-B before cluster-A's STS is `IsReady`.

#### M3 — AC publication and end-to-end deployment

**Goal:** Leader publishes AutomationConfig to OM; agents pick up; mongod processes start; sharded cluster is queryable.

**Deliverables:** full item 7 (AC publication path), item 11.

**Acceptance:**
- `scripts/poc/smoke-test.sh` runs end-to-end:
  - Apply CR to all 3 clusters.
  - Wait for all 3 operators to report `phase: Running` in their CR status.
  - `mongosh` connects to mongos in cluster-A; runs `sh.status()`; sees the shard listed.
  - Insert a document; query it; verify it returns.

#### M4 — DR drill

**Goal:** Kill the leader's pod mid-operation; verify a new leader is elected and the in-flight plan resumes correctly.

**Deliverables:** item 12.

**Acceptance:**
- Apply an image-bump CR change (e.g., bump mongod version).
- While the resulting plan is mid-execution (one cluster's STS is rolling, others pending), `kubectl delete pod operator-A` (assuming A is leader).
- New leader is elected within 5s (Raft).
- The new leader reads `currentPhase` from Raft FSM; allocates the next lease appropriately.
- Plan completes; smoke-test passes.
- Repeat with the leader's pod's emptyDir being lost (kubectl delete + recreate). Verify operator-A rejoins as a fresh Raft node, catches up via snapshot install, and resumes participation. (This validates the emptyDir recovery path.)

### 20.6 What the PoC proves

| Claim | Validated by |
|---|---|
| Per-cluster operators can form a Raft cluster over mTLS | M0 |
| Content-hashed spec agreement works | M1 |
| GitOps drift is detected and refused | M1 |
| Cross-cluster STS serialization via leases | M2 |
| Leader-only AC publication | M3 |
| No cross-cluster K8s API access required | M0–M3 (no kubeconfig Secret in operator) |
| End-to-end multi-cluster sharded MongoDB is operational | M3 |
| Leader failover preserves plan state | M4 |
| emptyDir loss recovery via Raft snapshot install | M4 |

### 20.7 What the PoC explicitly does NOT prove

- Durability across simultaneous multi-pod loss.
- Production rollout / upgrade safety.
- Cert rotation flows.
- OnDelete pod-restart serialization (intra-cluster).
- 2-cluster (witness voter) topology.
- Migration from hub-spoke.
- Backup/restore of Raft state.
- Behavior under sustained network partitions.
- Search workload.
- Scale (only one shard, one mongos, 3 replicas).
- ConfigMap-/Secret-backed storage backend.

These belong to a post-PoC hardening track.

### 20.8 Out-of-band setup (one-time per dev environment)

- 3 kind clusters created via `setup-kind-3cluster.sh`. Use kind's `extraPortMappings` to expose each operator's Raft port to the host.
- Ops Manager dev instance reachable from all 3 clusters (existing dev infrastructure).
- Bootstrap CA generated by a small script (`openssl` one-liner is fine for PoC); 3 mTLS leaf certs issued from it; each leaf packaged as a K8s Secret in its respective cluster.
- A single seal-key (32 random bytes) generated and packaged identically into each cluster as a K8s Secret (`raft-seal-key`).
- The operator Helm chart installs both Secrets and configures the operator deployment to read them at startup.

### 20.9 Demo script (the canonical run)

```
# One-time setup
./scripts/poc/setup-kind-3cluster.sh
./scripts/poc/install-operators.sh

# Apply the CR to all clusters
for c in cluster-a cluster-b cluster-c; do
  kubectl --context=$c apply -f scripts/poc/sharded-cluster.yaml
done

# Watch agreement and plan execution
./scripts/poc/watch.sh   # tail logs from all 3 operators side by side

# Run smoke test
./scripts/poc/smoke-test.sh

# DR drill
./scripts/poc/dr-drill.sh
```

A successful run produces:

```
[operator-A] Raft state: Leader, term=3
[operator-A] Agreed spec generation=1, hash=sha256:abcd...
[operator-A] Created plan id=p-1, phases=[apply-sts(shard-0,A), apply-sts(shard-0,B), apply-sts(shard-0,C), apply-sts(cfg,A), ..., publish-ac, wait-agents]
[operator-A] Allocated lease: {phase=0, cluster=A}
[operator-A] Holding lease; CreateOrUpdate sts/mongodb-sharded-shard-0
[operator-A] STS ready; LeaseComplete proposed
[operator-A] Allocated lease: {phase=1, cluster=B}
[operator-B] Holding lease; CreateOrUpdate sts/mongodb-sharded-shard-0
...
[operator-A] PublishDeployment to OM (leader-only)
[operator-A] WaitForReadyState: all agents goal-reached
[operator-A] Plan p-1 complete
[smoke-test] mongosh insert/query OK
[dr-drill] kubectl delete pod operator-A
[operator-B] Raft state: Leader, term=4 (took over)
[operator-B] Resuming plan p-2 from phase=2
[smoke-test] After change: mongosh insert/query OK
```

### 20.10 Risks and unknowns

- **kind cross-cluster networking is the biggest setup risk.** If `NodePort` reachability between kind clusters proves fragile, fall back to running 3 operators as separate pods within a single kind cluster with separate namespaces and `kubeconfig`-per-namespace (loses the "no cross-cluster K8s API" demonstration but proves the Raft+lease protocol). Decide at end of M0.
- **OM-side coordination assumptions.** PoC assumes leader's AC publication and OM's agent convergence works the same way as today. If there's any OM-side per-cluster expectation (e.g., OM expecting requests from a specific source IP), surface it in M3.
- **Sealed entries in PoC are single-key.** No rotation; the `keyId` field is present but always `"k1"`. Rotation is post-PoC.
- **`raft.RecoverCluster` from emptyDir loss.** If a pod's emptyDir is wiped and the operator restarts, it needs clean rejoin logic. M4 explicitly tests this — if it doesn't work, the PoC has revealed a real design hole and the post-PoC plan must address storage durability before any wider rollout.

### 20.11 Definition of done

The PoC is done when:

- `scripts/poc/setup-kind-3cluster.sh && scripts/poc/install-operators.sh && scripts/poc/smoke-test.sh && scripts/poc/dr-drill.sh` runs green from a clean checkout on a dev machine.
- The operator code path for distributed mode is gated behind `--mode=distributed` and does not affect the existing hub-spoke code path. (Old behavior is unchanged.)
- A short demo recording (~5min) walks through the smoke test + DR drill, narrating what's happening at each step.
- A follow-up document captures the surprises encountered, the open hardening questions, and the recommended next milestones.
