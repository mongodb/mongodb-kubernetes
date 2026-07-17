# Reverse Migration v2 — delete-free handover with plain-deletion fallback

**Status:** supersedes Procedure 3 of `2026-07-02-appdb-mongodb-cr-reference-design.md`. Forward
migration (Procedure 2) and Fresh Start (Procedure 1) are unchanged.

## Problem with Procedure 3 v1

v1 made **CR deletion the trigger** for reverse migration: an `appdb-detach` finalizer stripped
the CR's OwnerReference from the StatefulSet and set `mongodb.com/appdb-migration-ready`, which
the OM controller's re-adoption gate consumed. Live testing exposed structural problems:

1. Shared handover secrets (`<om>-db-om-password`, `<om>-db-keyfile`) created by the CR carry the
   CR's OwnerReference and are **garbage-collected on CR deletion**, mid-migration — the internal
   AppDB reconciler then regenerates them, rotating the cluster keyfile while old-generation pods
   still run the previous key: `__system` SCRAM `storedKey mismatch`, the replaced member sticks
   in `RECOVERING`, and the rolling reshape deadlocks.
2. Deletion-as-trigger conflates two user intents (migrate back vs. deprovision), forcing
   role-specific special cases into `OnDelete` and the finalizer.

## v2 flow

### Graceful path (zero downtime; the CR outlives the migration)

1. **User reconfigures the OM only**: removes `spec.externalApplicationDatabaseRef` and adds
   `spec.applicationDatabase` in the same update (validation already requires exactly one of the
   two). The MongoDB CR is *not* deleted.
2. **OM internal AppDB reconciler (gate, start of `ReconcileAppDB`)** evaluates the StatefulSet
   named `Spec.AppDB.Name()`:
   - **owned by this OM** → already adopted, proceed.
   - **not found** → recreate-from-scratch path (see fallback below), proceed.
   - **foreign-owned (the MongoDB CR)** → set annotation
     **`mongodb.com/appdb-reverse-migration-ready: "true"`** on the StatefulSet (idempotent),
     return Pending ("waiting for MongoDB controller to release AppDB StatefulSet").
   - **ownerless** → adopt: set the OM's OwnerReference, remove
     `appdb-reverse-migration-ready` (and any stale `appdb-migration-ready`) in the same update,
     proceed with the reshape. Ownerless means nobody manages the StatefulSet — this also
     gracefully absorbs an aborted forward migration (post-detach state) once the ref is removed.
     Removal at adoption (not at migration completion) is deliberate and symmetric with the
     forward direction, where the CR's `consumeAdoptionSignal` deletes `appdb-migration-ready`
     immediately after its gate passes: from adoption onward, the OwnerReference is the
     authoritative "migration in progress/complete" state, and a lingering annotation would trip
     the CR gate's defensive condition if the user aborts mid-reshape back to forward migration.
3. **MongoDB CR reconciler (role: AppDB, before its adoption gate)**: if its own StatefulSet
   carries `appdb-reverse-migration-ready` **and** the CR's own OwnerReference, strip the
   OwnerReference (single update; the annotation stays), then report Pending
   ("released AppDB StatefulSet to Ops Manager; this resource can be deleted"). Reconcile stops
   there — the CR no longer manages the StatefulSet.
4. **OM's next pass** finds the StatefulSet ownerless (+ its own annotation) → adopts per step 2,
   reshapes the pod template to the internal-AppDB form, pods roll one by one. Cluster keyfile
   and user password continuity hold via the shared secrets (below), so mixed-generation
   replication keeps authenticating.
5. **CR-side blocking after release**: during the release window the StatefulSet is ownerless
   but carries no `appdb-migration-ready`, so the CR's existing two-signal `checkAdoptionGate`
   already blocks re-adoption. `checkAdoptionGate` additionally gains a defensive condition —
   blocked while `appdb-reverse-migration-ready` is present — guarding against a stale,
   unconsumed `appdb-migration-ready` coexisting with a release request. After OM adoption, the
   existing foreign-owner condition keeps the CR blocked.
6. **User deletes the CR afterwards** (ordinary deletion, no finalizer): everything the CR still
   owns is garbage-collected. The StatefulSet, services, and shared secrets are by then OM-owned
   or ownerless, so nothing the OM depends on is touched. `OnDelete` keeps skipping
   `cleanOpsManagerState` for role-AppDB CRs (decided: the managing project keeps a stale
   automation config; harmless bookkeeping debt).

### Abort path

If the user re-adds `externalApplicationDatabaseRef` before the OM adopted: the external
reconciler **swaps the annotations** on the ownerless StatefulSet — removes
`appdb-reverse-migration-ready`, sets `appdb-migration-ready` — putting it in exactly the state a
forward-migration detach produces. The CR's two-signal gate then passes (ready + ownerless), it
consumes the annotation via `consumeAdoptionSignal`, and re-adopts. (Removing
`reverse-migration-ready` alone would leave the CR blocked forever: an ownerless StatefulSet
without `migration-ready` never satisfies its gate.)

### Fallback path (CR deleted first; downtime accepted, data preserved)

Deleting the CR at any point is plain Kubernetes deletion — **no finalizer, no detach**:

- The StatefulSet (CR-owned) and services are garbage-collected; pods terminate; **the AppDB and
  the Primary OM go down** for the duration of the gap. The OM in external mode reports Failed
  (ref validation: CR not found) — the user's signal to finish reconfiguring.
- **PVCs survive**: database StatefulSets use the Kubernetes default
  `persistentVolumeClaimRetentionPolicy` (`whenDeleted: Retain`; the operator only overrides this
  for search), and PVCs carry no OwnerReferences. Data is intact. (Caveat: a CR with
  `persistent: false` genuinely loses data — document.)
- Once the user reconfigures the OM (ref removed + `applicationDatabase` added), the gate finds
  no StatefulSet → creates one from scratch. Names match (`<om>-db`, volumeClaimTemplate `data`),
  so the new StatefulSet **re-binds the retained PVCs by name**. The shared secrets survived
  deletion (ownerRef-free, below), so the recreated automation config reuses the same cluster
  keyfile and user password — all members restart with uniform auth material; the
  `storedKey mismatch` class of failure cannot occur (it requires mixed generations).

### Shared handover secrets — ownership follows the AppDB's manager

`<om>-db-om-password` and `<om>-db-keyfile` are owned by **whichever controller currently
manages the AppDB**, and ownership transfers at each handover alongside the StatefulSet:

- **CR-created (fresh start)**: `ensureAppDBRoleUser` / `ensureAppDBRoleKeyfile` create them with
  the **CR's OwnerReference** (normal resource semantics — deleting the CR deletes them).
- **Forward migration**: the OM-side detach
  (`stripInternalAppDBOwnerReferencesFromSecretsAndConfigMaps`) strips the OM's refs from both
  secrets (the keyfile secret is added to the strip list — today only the password secret is
  stripped), and the CR's `ensureAppDBRole*` steps **claim** existing secrets by setting the CR's
  OwnerReference.
- **Reverse migration (graceful)**: the internal AppDB reconciler **claims** both secrets for the
  OM at adoption (explicit claim step — the existing `createOrUpdateSecretIfNotFound` only
  creates, never re-owns). The final post-handover CR deletion then garbage-collects nothing the
  running AppDB uses.
- **Reverse migration (fallback, CR deleted first)**: the secrets are garbage-collected together
  with the StatefulSet — deliberately. The recreate-from-scratch path generates fresh
  credentials **uniformly across all members** (no mixed generations, so no `storedKey mismatch`
  class of failure), re-binds the retained PVCs, and the agents repair the on-disk user passwords
  via `__system`/keyfile auth. Credential rotation is an accepted property of this path.

### Machinery deleted (v1 leftovers)

- `appDBDetachFinalizer`, `ensureAppDBFinalizer`, `cleanupAppDBFinalizer`, and the
  deletion-timestamp branch in the MongoDB controller's Reconcile.
- The OM-side re-adoption no longer consumes `appdb-migration-ready` (that annotation remains
  forward-migration-only: set by `detachInternalAppDB`, consumed by the CR's
  `consumeAdoptionSignal`). `detachInternalAppDB` additionally clears a stale
  `appdb-reverse-migration-ready` defensively.
- The ownerRef-free secret creation from the v1 bugfix iteration is superseded by the
  ownership-transfer model above.

## Annotation summary

| Annotation | Set by | Consumed/removed by | Meaning |
|---|---|---|---|
| `mongodb.com/appdb-migration-ready` | OM external reconciler (`detachInternalAppDB`) | MongoDB CR (`consumeAdoptionSignal`) | forward: internal AppDB detached, CR may adopt |
| `mongodb.com/appdb-reverse-migration-ready` | OM internal AppDB reconciler (gate) | OM internal AppDB reconciler (on adoption); external reconciler (on abort, swapped to `appdb-migration-ready`) | reverse: OM requests release; CR strips its OwnerReference in response |

## Event delivery note

The MongoDB controller's StatefulSet watch predicate (`PredicatesForStatefulSet`) forwards
changes to `appdb-reverse-migration-ready` — the release request is an annotation-only update
that the readiness-based filtering would otherwise drop, leaving the CR reconciler asleep on its
24h requeue.

## Testing

- **Unit**: OM gate state machine (owned / not-found / foreign → annotate+Pending / ownerless →
  adopt+clean annotations); CR release step (strips ownerRef exactly once, annotation preserved);
  CR gate blocked while `reverse-migration-ready` present; abort (external reconciler clears the
  annotation); secrets created ownerRef-free; detach strips keyfile secret; finalizer removal
  (deletion completes without it).
- **E2E** (both reverse classes): reconfigure OM with the CR present → assert CR reaches its
  released Pending state → internal AppDB resumes (zero pod-gap in the graceful path) → delete CR
  → assert OM-owned resources and shared secrets untouched (same `creationTimestamp`). The
  "password secret unchanged" assertion is a **graceful-path property only**. The forward file's
  delete-first flavour becomes the fallback test: delete CR → downtime → reconfigure → recreate
  re-binds PVCs → sentinel document survives despite rotated credentials (data-preservation
  proof; no password/keyfile-stability assertions on this path).

## Open items

1. The CR's released-state message and phase (Pending vs a dedicated condition) — cosmetic,
   decide at implementation.
2. Whether `reAdoptInternalAppDBIfNeeded` keeps its name (it no longer only "re-adopts"; it
   arbitrates ownership) — rename candidate: `ensureAppDBStatefulSetOwnership`.
3. `persistent: false` role-AppDB CRs: consider a validation warning (data loss on fallback path).
