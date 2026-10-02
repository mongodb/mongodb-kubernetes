# External AppDB — Reverse Migration: step-by-step walkthrough

This guide migrates an **existing** Ops Manager from an **external** Application Database (a `MongoDB`
`role: AppDB` CR referenced via `spec.externalApplicationDatabaseRef`) **back to an
internally-managed** AppDB (`spec.applicationDatabase`) — gracefully, **without downtime and without
rolling the OM pods**. The Ops Manager re-adopts the existing AppDB StatefulSet, and the now-released
`MongoDB` CR is deleted afterwards.

It assumes the external-AppDB setup is already running (see Prerequisites) and only shows the
reverse-migration steps. Every step is a plain `kubectl` command with the resource YAML inlined.

> See also: [../01-fresh-start](../01-fresh-start) and [../02-forward-migration](../02-forward-migration).

## How it works

```
BEFORE                                                        AFTER
─────────                                                     ─────────
primary OM ──externalApplicationDatabaseRef──►┐               primary OM ──owns──► StatefulSet
 (no spec.applicationDatabase)                │                (spec.applicationDatabase)
                                              ▼
management OM ──manages──► MongoDB(role:AppDB) ──owns──► STS   MongoDB(role:AppDB)  → released, deleted
```

1. Starting state: the primary OM uses an external AppDB — a `MongoDB` (`role: AppDB`) CR named
   `<primary-om-name>-db` that owns the AppDB StatefulSet.
2. You remove `spec.externalApplicationDatabaseRef` and add `spec.applicationDatabase`. The operator
   asks the `MongoDB` CR to **release** the StatefulSet; the CR reports `Pending` (released/unmanaged).
   The operator then **re-adopts** the StatefulSet as an internally-managed AppDB. The connection
   string is unchanged (same hosts), so the OM pods do not roll.
3. After the handover the `MongoDB` CR is a leftover and is deleted.

## Prerequisites

This guide starts from an already-running external-AppDB deployment. Before you begin you must have:

- A Kubernetes cluster with the **MongoDB Kubernetes operator installed** and `kubectl` configured.
- A **primary Ops Manager** (`${PRIMARY_OM_NAME}`, below) currently using an **external AppDB** —
  i.e. it has `spec.externalApplicationDatabaseRef` pointing at a `MongoDB` (`role: AppDB`) CR named
  `<primary-om-name>-db`, and no internal `spec.applicationDatabase`. Its
  `status.applicationDatabase.phase` reads `Disabled`. **This guide does not cover reaching that
  state** — see [../01-fresh-start](../01-fresh-start) or [../02-forward-migration](../02-forward-migration).
- The **`MongoDB` (`role: AppDB`) CR** (`${APPDB_NAME}`) is `Running` and owns the AppDB StatefulSet.

Quick sanity check that the prerequisites are in place:

```bash
# primary OM references an external AppDB, and its own AppDB status is Disabled:
kubectl get om "${PRIMARY_OM_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" \
  -o jsonpath='ref={.spec.externalApplicationDatabaseRef.name} appdb={.status.applicationDatabase.phase}{"\n"}'
# -> ref=${APPDB_NAME} appdb=Disabled

# the MongoDB (role: AppDB) CR is Running and owns the StatefulSet:
kubectl get mdb "${APPDB_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" \
  -o jsonpath='{.status.phase}{"\n"}'
kubectl get statefulset "${APPDB_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" \
  -o jsonpath='{range .metadata.ownerReferences[*]}{.kind}/{.name}{"\n"}{end}'
# -> MongoDB/${APPDB_NAME}
```

## 0. Set your variables

```bash
export K8S_CTX="<your-kube-context>"     # kubectl config get-contexts
export MDB_NS="mongodb"                    # namespace of the existing OM and AppDB

export PRIMARY_OM_NAME="primary-om"        # the OM being migrated back to an internal AppDB
export APPDB_NAME="${PRIMARY_OM_NAME}-db"  # the external AppDB MongoDB CR / StatefulSet name

# Must match the AppDB version currently running (the version on the MongoDB role:AppDB CR / the
# StatefulSet being re-adopted). Reusing the same version keeps the same binaries and data in place.
export APPDB_VERSION="8.0.5-ent"
```

---

## 1. Reconfigure the primary OM to an internal AppDB

Remove `spec.externalApplicationDatabaseRef` (set it to `null`) and add `spec.applicationDatabase`.
The operator asks the `MongoDB` CR to release the StatefulSet, then re-adopts it. The computed
connection string is unchanged, so the OM pods do not roll.

```bash
kubectl patch om "${PRIMARY_OM_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" \
  --type merge \
  -p "{\"spec\":{\"externalApplicationDatabaseRef\":null,\"applicationDatabase\":{\"members\":3,\"version\":\"${APPDB_VERSION}\"}}}"
```

## 2. Wait for the release-and-adopt handover

First the `MongoDB` CR is released (becomes `Pending` / unmanaged), then the OM re-adopts the
StatefulSet and its internal AppDB returns to `Running`.

```bash
# the MongoDB CR is released:
kubectl wait --for=jsonpath='{.status.phase}'=Pending \
  mdb/"${APPDB_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" --timeout=300s
kubectl get mdb "${APPDB_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" \
  -o jsonpath='{.status.message}{"\n"}'

# the OM manages the internal AppDB again:
kubectl wait --for=jsonpath='{.status.applicationDatabase.phase}'=Running \
  om/"${PRIMARY_OM_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" --timeout=1200s
kubectl wait --for=jsonpath='{.status.opsManager.phase}'=Running \
  om/"${PRIMARY_OM_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" --timeout=1800s
```

## 3. Delete the released `MongoDB` CR

> **Wait for step 2 to finish first.** Only delete the `MongoDB` CR once the OM manages the internal
> AppDB again (step 2 shows `applicationDatabase.phase=Running`). Deleting it earlier — while it
> still owns the StatefulSet — would garbage-collect the AppDB.

The handover is complete and the Ops Manager owns the StatefulSet, so the released CR is a safe-to-
remove leftover:

```bash
kubectl delete mdb "${APPDB_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" --wait=true --timeout=300s
```

## 4. Verify

```bash
kubectl get pods --context "${K8S_CTX}" -n "${MDB_NS}"

# The AppDB StatefulSet must now be owned solely by the Ops Manager (not the MongoDB CR):
kubectl get statefulset "${APPDB_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" \
  -o jsonpath='{range .metadata.ownerReferences[*]}{.kind}/{.name}{"\n"}{end}'
# -> MongoDBOpsManager/${PRIMARY_OM_NAME}

# The primary OM's AppDB status is Running again (internally managed):
kubectl get om "${PRIMARY_OM_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" \
  -o jsonpath='{.status.applicationDatabase.phase}{"\n"}'
# -> Running
```

You should see the AppDB StatefulSet owned by `MongoDBOpsManager/${PRIMARY_OM_NAME}`, the primary OM's
internal AppDB `Running`, and the `MongoDB` CR gone — with the OM pods never having rolled during the
handover.

## Troubleshooting

- **`MongoDB` CR not released (stays `Running`, StatefulSet still owned by it)** — the OM must first
  request the release. Confirm the patch set `spec.externalApplicationDatabaseRef` to `null` **and**
  added `spec.applicationDatabase`.
- **Deleting the CR before the handover** — do not delete the `MongoDB` CR until step 2 shows the OM
  managing the internal AppDB again; deleting early (while it still owns the StatefulSet) would
  garbage-collect the AppDB. If you need the delete-first fallback path instead, see the operator
  documentation for recreate-from-retained-PVCs.
- **`applicationDatabase.version` mismatch** — set it to the version the AppDB is already running so
  the re-adopted StatefulSet keeps the same binaries; changing it here triggers an AppDB upgrade at
  the same time as the handover.
</content>
