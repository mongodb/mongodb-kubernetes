# Decoupling `mongodb-community-operator` from Enterprise — investigation report

Branch: `decuple-mdb-community` (no code changes; investigation only).
Date: 2026-05-11.

## Goal (per user)

End-state **C** (refined): single Go module (`github.com/mongodb/mongodb-kubernetes`), with a **unidirectional package boundary**:

- **Hard rule**: **no package outside `mongodb-community-operator/` may import any package under it** (with allowlisted exceptions, see below).
- **Relaxed direction**: MCO **may import packages outside** `mongodb-community-operator/` (including Enterprise `controllers/...`, `pkg/...`, `api/...`). This is a deliberate asymmetry confirmed by the user during review iteration 3.

**Decisions confirmed with user (2026-05-11):**

- **Shared logic that needs to be importable from both flavours lives at root `pkg/`** (or root `api/` for CRD types). The reason it lives at root is so that Enterprise can import it without violating the hard rule.
- **Allowlisted exceptions to the hard rule (outside → MCO imports that are kept)**:
  1. `main.go` imports `mongodb-community-operator/api/v1` solely for `AddToScheme` and controller manager wiring; **also** imports `mongodb-community-operator/controllers` solely for `NewReconciler` (the community `ReplicaSet` reconciler constructor) — discovered during Plan 6 implementation; the Plan 1 survey under-counted the symbols on that import. Both `api/v1` and `controllers` allowlist entries are needed for the Enterprise binary's MongoDBCommunity wiring.
  2. `controllers/searchcontroller/*.go` imports `mongodb-community-operator/api/v1` for `MongoDBCommunity` discovery (cluster 8.a, resolved).
  3. Resolved from cluster-7 sub-classification (Plan 7):
     - `controllers/operator/construct/*.go` may import `mongodb-community-operator/controllers/construct` for the AppDB-specific helpers `BuildMongoDBReplicaSetStatefulSetModificationFunction`, `AutomationAgentCommand`, `GetMongodbUserCommandWithAPIKeyExport`. These wrap MCO types and cannot be moved without dragging MCO types into Enterprise.
     - `main.go` may import `mongodb-community-operator/controllers/construct` for MCO-community-specific env vars: `VersionUpgradeHookImageEnv`, `ReadinessProbeImageEnv`, `MongoDBCommunityImageTypeEnv`, `MongodbCommunityImageEnv`, `MongodbCommunityRepoUrlEnv`, `DefaultImageType`.
     - All 12 other importers of `mongodb-community-operator/controllers/construct` were redirected at the new root `pkg/construct/imageconstants.go` (Plan 7's HYBRID outcome — see PR #1099 for details).
- **`MongoDBSearch` stays in Enterprise** (`api/v1/search/` + `controllers/searchcontroller/`). MCO's reconcile keeps importing it (MCO → outside is allowed). Earlier proposals to move search to root or into MCO are dropped. The only required cleanup is making `controllers/searchcontroller` stop importing MCO (mostly handled by clusters 1–6, 9, 10; the residual `MongoDBCommunity` reference is the open question).

## Headline

The dependency is **strongly one-directional**: Enterprise reaches deeply into MCO, MCO barely reaches back.

| direction | files crossing the boundary | distinct packages reached |
|---|---|---|
| Enterprise → MCO | **116** | 28 |
| MCO → Enterprise | **7** (3 prod, 4 test) | 4 |

Under the unidirectional rule, the **MCO → Enterprise** side requires no work at all (any current import is now legal). All decoupling effort is on the **Enterprise → MCO** side, dominated by **AppDB** in `controllers/operator/`. Several Enterprise packages that currently import MCO heavily (`pkg/statefulset`, `controllers/searchcontroller`, `api/v1/search`) must have those MCO imports removed; the MCO → those-packages direction does not need to change.

Top five MCO subpackages by Enterprise importer count (counts captured 2026-05-11; re-run grep before starting implementation as the codebase moves):

1. `pkg/kube/client` — ~47-51
2. `api/v1/common` — ~32-36
3. `pkg/automationconfig` — ~27
4. `api/v1` (MCO CRD) — ~21
5. `pkg/util/merge` — ~20-23

(Spot-checks during review iteration 2 showed counts had drifted upward by 5-10% on the heaviest packages — direction and ordering unchanged; the absolute numbers are investigation-grade approximations.)

Packages with **zero Enterprise importers** (already MCO-leaf, no decoupling action needed): `pkg/readiness/*`, `pkg/helm`, `pkg/authentication` (root + `x509`, `mocks`), `pkg/util/{contains,functions,versions,status}`, `pkg/kube/{pod,resourcerequirements}`, `controllers/{watch,predicates,validation}`, `test/e2e/*`, `cmd/readiness/testdata`.

## AppDB footprint (heaviest single consumer)

5 AppDB files in Enterprise (`controllers/operator/appdbreplicaset_controller{,_test,_multi_test}.go`, `controllers/operator/construct/appdb_construction{,_test}.go`) reach into 14+ MCO packages. Call-site counts below are scoped to **the dominant single AppDB file per row** (usually `appdbreplicaset_controller.go` or `appdb_construction.go`) — they are indicative of weight, not summed across all five files. Where the cross-file total differs materially, both are listed.

| MCO subpackage | call sites (dominant AppDB file) |
|---|---|
| `pkg/automationconfig` | ~51 in `appdbreplicaset_controller.go`; ~69 across all 5 AppDB files |
| `pkg/kube/podtemplatespec` | ~38 |
| `pkg/kube/container` | ~26 |
| `pkg/kube/configmap` | 12 |
| `pkg/util/merge` | ~9 |
| `pkg/kube/client` | 7 |
| `pkg/util/scale` | 7 |
| `pkg/kube/secret` | 4 |
| `pkg/kube/annotations` | 3 |
| `pkg/agent` | 3 |
| `controllers` (root MCO) | 2 |
| `controllers/construct` | 2 |
| `api/v1` (`mdbcv1`) | 2 |
| `api/v1/common` | 6 |
| `pkg/authentication/scram` | 1 |

Any decoupling plan must address AppDB first; everything else is a long tail.

---

## Outside → MCO edges to break

Two Enterprise-side packages currently violate the hard rule and must have their MCO imports removed:

1. **`pkg/statefulset` (Enterprise) → MCO**
   `pkg/statefulset` imports `mongodb-community-operator/pkg/kube/{annotations,client}` and `mongodb-community-operator/pkg/util/merge`. These get redirected at root `pkg/kube/...` and root `pkg/util/merge` after clusters 1 and 5 land. MCO files that import `pkg/statefulset` (the reverse direction) are now allowed and need no change.

2. **`controllers/searchcontroller` + `api/v1/search` (Enterprise) → MCO**
   These two Enterprise packages collectively import ~10 MCO packages: `api/v1`, `api/v1/common`, `pkg/automationconfig`, `pkg/kube/{client,container,podtemplatespec,secret,service,probes}`, `pkg/mongot`, `pkg/tls`. After clusters 1–6, 9, 10 land, all but `api/v1` (MCO's `MongoDBCommunity` CRD) are redirected at root. The residual `MongoDBCommunity` import is handled via the cluster 8.a depguard allowlist. MCO's `replica_set_controller.go` importing search code is now allowed and needs no change.

---

## Cluster-by-cluster plan

For each cluster, the **strategy** is chosen under the unidirectional constraint plus the user's "shared logic at root `pkg/`" rule. Options:

- **MOVE→`pkg/`** — relocate from inside `mongodb-community-operator/` to a top-level shared package (root `pkg/...` for code, root `api/...` for CRD types). Enterprise can then import the same code without crossing the boundary. **Default for anything genuinely reused.**
- **MOVE→ENTERPRISE** — keeps Enterprise as the owner; MCO can still import it (allowed direction).
- **KEEP IN MCO** — stays inside MCO. Outside consumers must rewrite locally, drop the call, or be added to the depguard allowlist.
- **DUPLICATE** — accept two copies (when the logic is genuinely diverging, or so small the cost is in the noise, or to avoid forcing a shared package).
- **INLINE / DROP** — small/dead enough to absorb at call sites.

### 1. `pkg/kube/*` builder helpers — kube helpers cluster

Packages: `client` (47), `secret` (18), `configmap` (16), `container` (12), `annotations` (12), `podtemplatespec` (8), `service` (6), `probes` (5), `lifecycle` (2), `persistentvolumeclaim` (2). No MongoDB semantics — pure k8s sugar.

**Strategy: MOVE→`pkg/kube/...`** at repo root.
- Largest raw volume in the survey (~130 importers combined) but mechanically simple.
- Both operators legitimately need these; duplicating ~12 builder packages bit-for-bit is wasteful.
- A flat move with import-path rewrite is a single mechanical refactor (`gofmt -r` / `goimports` after path swap).
- **Known name collision: `pkg/kube/service`**. Both Enterprise (`pkg/kube/service`) and MCO (`mongodb-community-operator/pkg/kube/service`) already have a `service` package. They must be reconciled during the move. Default approach: inspect both APIs; if compatible, merge into a single `pkg/kube/service` (MCO's is the more complete builder DSL, Enterprise's appears to be a thinner wrapper — prefer MCO's API surface and port the Enterprise consumers to it). If incompatible, rename one (MCO's stays as `pkg/kube/service`; Enterprise's becomes `pkg/kube/svcops/` or similar). Confirm during implementation planning.
- Enterprise `pkg/kube/` is otherwise sparse (`commoncontroller`, `kube.go`, `service`); no other collisions.

### 2. `pkg/automationconfig` — agent contract

27 Enterprise importers (heaviest in `controllers/om` and AppDB). This package is the Go schema for the MMS automation-config JSON.

**Strategy: MOVE→`pkg/automationconfig/`** at repo root.
- Both sides emit this JSON; the agent reads only one wire format. Duplicating risks silent drift on a field that breaks production agents.
- This is the highest-risk cluster if duplicated. Move it out, keep one source of truth.
- Generated deepcopy (`mongodb-community-operator/pkg/automationconfig/zz_generated.deepcopy.go`) moves with the types.

### 3. `api/v1/common` — shared CRD wrappers

32 Enterprise importers. Provides embeddable structs (`StatefulSetConfiguration`, `Persistence`, `PersistenceConfig`, `ServiceSpecWrapper`, `StatefulSetSpecWrapper`, `PodTemplateSpecWrapper`) — already embedded in Enterprise CRDs (`MongoDB`, `MongoDBOpsManager.AppDB`, `MongoDBSearch`).

**Strategy: MOVE→`api/common/v1/`** (or `api/v1/common/` at root — bikeshed; not under `mongodb-community-operator/api/`).
- These structs are genuinely shared CRD vocabulary; they are baked into JSON schemas on both sides.
- Duplicating them re-derives JSON tags and risks tag drift, which is silently dangerous for CRDs.
- Move once, both sides import the same path. Generated deepcopy moves with the types.
- **Open detail**: exact import path needs a quick decision. Recommend `api/v1/common/` at repo root, mirroring the current MCO layout, to minimise the diff on Enterprise CRDs that already embed these types.

### 4. `api/v1` (MCO `MongoDBCommunity` CRD) — types/api

21 Enterprise importers: `main.go` (scheme registration), `controllers/searchcontroller` (3 — for the community-source case), telemetry (2), AppDB (2), Enterprise `api/v1` (6 — cross-CRD type references).

**Strategy: KEEP IN MCO; Enterprise stops importing it, except for the documented `main.go` scheme-registration exception.**
- The `MongoDBCommunity` CRD belongs to MCO.
- Enterprise references resolve as:
  - **`main.go` scheme registration**: **accepted exception** (confirmed with user). This single import remains cross-boundary and must be whitelisted by the CI guard.
  - **`controllers/searchcontroller` (3 importers)**: goes away when search moves to shared `pkg/search/` (cluster 8) — the shared search code still talks to the MCO CRD, but it lives at root, so `pkg/search → mongodb-community-operator/api/v1` is a legal root → MCO edge, not Enterprise → MCO.
  - **Cross-CRD type references in Enterprise `api/v1` (6 importers)**: usually carrying an `mdbcv1.AuthMode` or similar — replace with the shared `api/v1/common` equivalent or copy the small enum into Enterprise.
  - **Telemetry (2 importers)**: counting `MongoDBCommunity` resources. Either runs through a shared registry or reads via dynamic client / unstructured to avoid the import.
  - **AppDB (2 importers)**: carries `mdbcv1.AutomationConfigOverride` etc. — CRD-spec types AppDB embeds. Preferred: relocate these specific types to shared `api/v1/common` (keeps schema in sync). Fallback: duplicate into Enterprise.

### 5. `pkg/util` — utility cluster

| pkg | imp. | strategy |
|---|---|---|
| `pkg/util/merge` | 20 | **MOVE→`pkg/util/merge/`** (operates on k8s specs; rides with kube cluster) |
| `pkg/util/scale` | 11 | **MOVE→`pkg/util/scale/`** (scaling state-machine; both operators run it) |
| `pkg/util/envvar` | 7 | **MOVE→`pkg/util/envvar/`** — trivial code, but already shared |
| `pkg/util/constants` | 5 | **MOVE→`pkg/util/constants/`** *or* duplicate; recommend move for parity |
| `pkg/util/apierrors` | 2 | **MOVE→`pkg/util/apierrors/`** or inline into Enterprise (only 2 sites) |
| `pkg/util/result` | 1 | **INLINE** (1 call site) |
| `pkg/util/generate` | 1 | **INLINE** (1 call site) |
| `pkg/util/{contains,functions,versions,status}` | 0 | MCO-leaf; no action |

`merge` and `scale` are the only non-trivial members; they ride with the kube cluster as part of step 3 of the recommended order.

### 6. Authentication — `pkg/authentication/*`

| pkg | Enterprise imp. | strategy |
|---|---|---|
| `pkg/authentication/scram` | 1 (AppDB) | **INLINE** `scram.Enable` into Enterprise auth glue (cheap; 1 call) |
| `pkg/authentication/scramcredentials` | 1 (test) | **INLINE** into the one Enterprise test, or duplicate the small file |
| `pkg/authentication/authtypes` | 1 (`api/v1/om/appdb_types.go`) | **INLINE** the one type into Enterprise (`api/v1/om/`), or drop the cross-ref |
| `pkg/authentication` (root), `x509`, `mocks` | 0 | MCO-leaf |

All four cross-boundary touches are 1 call site each, so inline beats move-to-shared here. SCRAM credential math is deterministic — if both sides need it later, promote to `pkg/authentication/scramcredentials/`.

### 7. `controllers/construct` and `controllers` (root MCO package)

- **`controllers/construct` (14 Enterprise importers, 11 in `controllers/operator`, 2 in `pkg/images`, 1 in `main.go`)**: this is the **MCO StatefulSet construction** that Enterprise re-uses for AppDB.
  **Strategy: DUPLICATE** — give Enterprise its own copy at `controllers/operator/construct/appdb_construct/`, owned by the AppDB controller. **Pending investigation** during implementation planning, which may flip the strategy to MOVE→`pkg/construct/` if the inspection shows the two sides are still essentially identical.
  - **Rationale for defaulting to duplicate (user preference)**: AppDB's construction has already accreted overlays specific to AppDB's environment (OM-side concerns, AppDB-specific volumes, agent flavours). Forcing a single source of truth would either (a) bloat `pkg/construct/` with optional knobs to satisfy both consumers, or (b) require Enterprise to wrap MCO's construct with a layer that adds the AppDB-specific shape — itself an effective duplication via composition. The user prefers an honest fork.
  - **Confirmation investigation** (must run during implementation planning, before this step starts): diff `mongodb-community-operator/controllers/construct/*.go` against the AppDB-side overlay (`controllers/operator/construct/appdb_construction.go` and the helpers it calls in `controllers/operator/construct/database_construction.go`). If the AppDB-relevant code path is essentially the MCO path with no semantic deltas, propose MOVE→`pkg/construct/` instead. Otherwise commit to the duplicate.
  - Note: even under duplicate, `pkg/images`'s 2 importers and `main.go`'s 1 importer may not need the AppDB-specific fork — they may be small enough to either keep using MCO's package (allowed: outside → MCO via allowlist) or import their own thin helper. The investigation should also classify which of the 14 importers need the duplicate vs. which can be cleanly cut over.
- **`controllers` root package (2 Enterprise importers)**: only two symbols (`OverrideToAutomationConfig`, `ListenAddress`). **Strategy: INLINE** into Enterprise — copy the 2 functions, drop the import. Quick win.

### 8. `MongoDBSearch` CRD + `controllers/searchcontroller` — **revised under unidirectional rule**

Decision: **search code stays in Enterprise where it is today**. MCO's `replica_set_controller.go` continues to import Enterprise's `api/v1/search` and `controllers/searchcontroller` — that direction is now allowed.

**Strategy:**
- **No relocation.** `api/v1/search/` stays under Enterprise's `api/v1/`. `controllers/searchcontroller/` stays under Enterprise's `controllers/`.
- **Cleanup of `controllers/searchcontroller`'s MCO imports** is required (it currently imports ~11 MCO packages). After clusters 1, 2, 3, 5, 9, 10 land, the search controller's MCO imports collapse to a single residual: `mongodb-community-operator/api/v1` (the `MongoDBCommunity` CRD type, used for the community-source case).
- **Cleanup of `api/v1/search`'s MCO imports** is required for the same reason: `api/v1/search/mongodbsearch_types.go` and the generated `zz_generated.deepcopy.go` import `mongodb-community-operator/api/v1/common`. Resolved automatically by cluster 3 (`api/v1/common` moves to root).
- **Residual — `MongoDBCommunity` references inside `controllers/searchcontroller`** (outside → MCO under the unidirectional rule): **resolved → 8.a Allowlist.** Add `controllers/searchcontroller/*.go → mongodb-community-operator/api/v1` to the depguard allowlist alongside `main.go`. Rationale: the search controller legitimately needs to discover and react to `MongoDBCommunity` resources; this is a deliberate cross-product hook and keeping static type safety is worth one allowlist line.

Order: cluster 3 (`api/v1/common` to root) must land before this cluster's residual cleanup, otherwise `api/v1/search` still pulls MCO in via the deepcopy file.

### 9. `pkg/mongot`

3 Enterprise importers (search controller + a test). Generic mongot YAML config — used by the shared search code.

**Strategy: MOVE→`pkg/mongot/`** at repo root. Rides with cluster 8 (shared search needs it).

### 10. `pkg/tls`

1 Enterprise importer (`controllers/searchcontroller`). Used by the shared search code post-move.

**Strategy: MOVE→`pkg/tls/`** at repo root (rides with cluster 8/9). If we discover the single helper is the only thing referenced, fall back to inlining it into `pkg/search/`.

### 11. `pkg/agent`

1 Enterprise importer (`appdbreplicaset_controller.go` calling `agent.AllReachedGoalState`).

**Strategy: MOVE→`pkg/agent/`** at repo root. Health-file JSON contract is shared with the agent regardless; keeping one Go consumer of that contract avoids drift. The whole `pkg/agent/` package (mostly readiness helpers) can move; what stays MCO-internal is the readiness *probe binary* under `pkg/readiness/` and `cmd/readiness/`.

### 12. MCO → Enterprise side — **dropped under unidirectional rule**

Under the original bidirectional reading, this cluster covered the 7 MCO files that imported Enterprise. Under the relaxed unidirectional rule, **MCO → Enterprise imports are legal**, so this entire cluster requires no action.

For traceability, the files that previously needed work and their new status:

| Enterprise pkg | MCO importers | status |
|---|---|---|
| `pkg/statefulset` (6 MCO importers) | 3 prod, 3 test | No action: MCO → Enterprise is allowed. (Note `pkg/statefulset` still needs its own MCO imports cleaned up — that's row 1 of the "Outside → MCO edges to break" section above, handled by clusters 1 and 5.) |
| `pkg/util` constants (2 MCO importers) | 1 prod, 1 test | No action. The 3 scalar leaks (`util.AgentContainerUtilitiesName`, `util.DefaultPodTerminationPeriodSeconds`, `util.OperatorName`) stay where they are. |
| `api/v1/search` (1 MCO importer) | prod | No action. MCO importing search is allowed; search stays in Enterprise (cluster 8). |
| `controllers/searchcontroller` (1 MCO importer) | prod | No action. Same as above. |

The depguard rule still has to enforce that the inverse direction (these Enterprise packages importing MCO) is clean — that work is captured in clusters 1, 3, 5, 8.

---

## Recommended order

Aiming to land mechanical changes first and isolate behavioural/design calls. Every "move" is to root `pkg/` (or `api/`). The unidirectional rule simplifies the work: there is no MCO → Enterprise cleanup, and the search tangle (cluster 8) collapses from a relocation to an in-place dependency cleanup.

1. **Move `pkg/kube/*` + `pkg/util/{merge,scale,envvar,constants,apierrors}` to root `pkg/`** — one big mechanical refactor (path swap + `goimports`). Eliminates the `pkg/statefulset` and most search-controller MCO imports as a side-effect. Includes the `pkg/kube/service` collision resolution (see cluster 1).
2. **Move `pkg/automationconfig` to root `pkg/automationconfig/`** — high-leverage, removes 27 outside → MCO edges and avoids drift on the agent contract. Generated deepcopy moves with it.
3. **Move `api/v1/common` to root `api/v1/common/`** — 32+ importers; mechanical but generated deepcopy must follow. Path bikeshed in Remaining decisions.
4. **Move `pkg/agent`, `pkg/mongot`, `pkg/tls` to root `pkg/`** — covers the remaining shared helpers Enterprise pulls from MCO.
5. **Cluster 8 residual — depguard allowlist** for `controllers/searchcontroller/*.go → mongodb-community-operator/api/v1`. Confirmed decision; added to the step-10 depguard config.
6. **Inline `controllers` root references** — copy `OverrideToAutomationConfig`, `ListenAddress` into Enterprise; drop the 2 imports. Quick win.
7. **Cluster 7 investigation + execution** — diff `mongodb-community-operator/controllers/construct/*.go` against `controllers/operator/construct/appdb_construction.go`. **Default: DUPLICATE** the relevant subset under `controllers/operator/construct/appdb_construct/` (Enterprise-side, owned by AppDB). **Confirm or flip to MOVE→`pkg/construct/`** based on the inspection. Sub-classify the 14 importers — some may not need the duplicate at all and can be redirected to the surviving MCO package via the allowlist if appropriate.
8. **Inline the auth long tail** (cluster 6) — 3 single call-site imports. **Also rolled in here** (per duplicate-audit follow-ups from PR #1088, deferred 2026-05-11):
   - **TC-1 — `SecretNotExist` deduplication.** Delete the byte-identical copy at `controllers/operator/secrets/secrets.go:144`; migrate ~20 call sites to `pkg/kube/secret.SecretNotExist`. Pure dead-code removal.
   - **TC-2 — `X509` constant rename.** `pkg/util/constants/constants.go:7` (value `"MONGODB-X509"`) collides by name with `pkg/util/constants.go:154` (value `"X509"`). Rename the former to `X509WireProtocol` (or similar) and update MCO callers.
   - **TC-3 — `AutomationAgentKeyFilePathInContainer` rename.** Same package-level collision pattern; values differ (`.../authentication/keyfile` vs `.../keyfile`). Rename the `pkg/util/constants/constants.go:8` version to `AutomationAgentAuthKeyFilePathInContainer`.
   - **TC-4 — `AutomationAgentWindowsKeyFilePath`.** Same name, identical value, benign. Optional remove of the duplicate in `pkg/util/constants/constants.go:13` if TC-3 is being touched anyway.
   - **Lint nits**: remove stale `// nolint:forbidigo` from `pkg/kube/container/containers.go:138`; remove the dead `envvar` `forbidigo` rule from `.golangci.yml` (the `mongodb-community-operator/pkg/util/envvar` path it forbids no longer exists).
9. **AppDB-specific `api/v1` references** (cluster 4 → AppDB row) — relocate the specific MCO CRD types AppDB embeds (e.g. `AutomationConfigOverride`) into the shared `api/v1/common`, or duplicate them on the Enterprise side.
10. **Add a CI guard** — recommended mechanism: a `golangci-lint` `depguard` rule with a **single list-mode rule**:
    - Files outside `mongodb-community-operator/` may not import any package under `github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/...`, with the following allowlisted exceptions:
      1. `main.go` may import:
         - `github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1` (scheme registration).
         - `github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/controllers` for `NewReconciler` (community `ReplicaSet` reconciler constructor used in controller-manager wiring).
         - `github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/controllers/construct` for the MCO-community-specific env-var constants used by the binary's image-resolution logic (`VersionUpgradeHookImageEnv`, `ReadinessProbeImageEnv`, `MongoDBCommunityImageTypeEnv`, `MongodbCommunityImageEnv`, `MongodbCommunityRepoUrlEnv`, `DefaultImageType`). Resolved from Plan 7.
      2. `controllers/searchcontroller/...` may import `github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1` for `MongoDBCommunity` discovery (cluster 8.a, confirmed).
      3. `controllers/operator/construct/...` may import `github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/controllers/construct` for the AppDB-specific helpers (`BuildMongoDBReplicaSetStatefulSetModificationFunction`, `AutomationAgentCommand`, `GetMongodbUserCommandWithAPIKeyExport`) — these wrap MCO types and cannot be moved without dragging MCO types into Enterprise. Resolved from Plan 7.

    Note: under the unidirectional rule, there is **no rule for the reverse direction** — MCO is free to import any non-MCO repo path.

    Implementation note: the file globs above are illustrative; depguard v2 syntax uses its own `files:` matcher language (`$test` / `!$test` qualifiers, `**`-style globs configured under `linters.settings.depguard.rules.<name>.files`). The translation is direct but the planner should use depguard's actual syntax.

    Toolchain status: `.golangci.yml` already ships in the repo and is wired into `make lint`, but `depguard` is **not** currently enabled (active linters: `dupl`, `errcheck`, `forbidigo`, `goconst`, `gosec`, `govet`, `ineffassign`, `rowserrcheck`, `staticcheck`, `unconvert`, `unused`). The implementation step is therefore "enable `depguard` in `linters.enable` and configure one list-mode rule under `linters.settings.depguard`". Alternatives (`go list` walker, custom `go vet` analyzer) require new infrastructure for the same outcome and are rejected.

After step 10 the boundary is enforced.

---

## Remaining decision points

These don't block planning — they're choices to resolve during step-by-step implementation planning:

1. **`api/v1/common` path** (cluster 3): `api/v1/common/` at repo root vs `api/common/v1/`. Recommend `api/v1/common/` for minimum diff against current MCO and Enterprise CRDs.
2. **Cluster 7 — duplicate vs move** (construct): default is **DUPLICATE** at `controllers/operator/construct/appdb_construct/`. Investigation during the implementation plan inspects whether the AppDB-side overlay has materially diverged from the MCO-side construct; if it has not, propose MOVE→`pkg/construct/` instead. Either way, sub-classify the 14 importers (some may use the allowlist for the small `pkg/images/` and `main.go` cases).
3. **Cluster 4 → AppDB embedded types**: relocate `mdbcv1.AutomationConfigOverride` etc. into shared `api/v1/common`, vs duplicate into Enterprise. Recommend relocate.

---

## Resolved (was: open questions)

1. **Enterprise main scheme-registration for `MongoDBCommunity`**: ACCEPTED EXCEPTION. depguard allowlists `main.go → mongodb-community-operator/api/v1`.
2. **Search ownership**: STAYS IN ENTERPRISE. MCO continues to import `api/v1/search` and `controllers/searchcontroller` (allowed under unidirectional rule). Search controller's own MCO imports are cleaned up by clusters 1–6, 9, 10; the residual `MongoDBCommunity` reference resolved via **8.a depguard allowlist** for `controllers/searchcontroller/*.go → mongodb-community-operator/api/v1`.
3. **Constraint interpretation**: **unidirectional** — outside → MCO is forbidden (with allowlisted exceptions); MCO → outside is allowed. Shared logic that Enterprise needs to import lives at root `pkg/` (or `api/`).
4. **MCO → Enterprise leaks** (3 scalar constants previously listed as cleanup): NO ACTION REQUIRED. Allowed under unidirectional rule.

---

## Sanity / methodology notes

- File counts come from `grep -rln --include='*.go' '"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator' .` and the symmetric query inside `mongodb-community-operator/`. Vendor and `mongodb-community-operator/` itself were filtered.
- The original prompt's "~224 importers" figure was a regex artefact; the corrected Enterprise-only count is **116**.
- No code was changed. No commits. Working branch: `decuple-mdb-community`.
