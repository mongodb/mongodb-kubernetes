# Multi-cluster External AppDB — Reverse Migration: step-by-step walkthrough

This guide migrates an Ops Manager from an **external** `MongoDBMultiCluster` AppDB (`role: AppDB`,
referenced via `spec.externalApplicationDatabaseRef`, `kind: MongoDBMultiCluster`) **back** to an
**internally-managed multi-cluster AppDB** (`spec.applicationDatabase` with `topology: MultiCluster`)
— **without rolling the OM pods**. The AppDB StatefulSets keep the same names and hosts, so the
connection string does not change.

It shows only the reverse-migration steps; to reach the external-AppDB starting state, follow
[../01-mc-fresh-start](../01-mc-fresh-start). Every step is a plain `kubectl` command with the resource
YAML inlined.

> This is the multi-cluster variant of [../../03-reverse-migration](../../03-reverse-migration). See
> also: [../01-mc-fresh-start](../01-mc-fresh-start) and
> [../02-mc-forward-migration](../02-mc-forward-migration).

> **Prerequisite — operator support.** Requires an operator build supporting
> `spec.externalApplicationDatabaseRef.kind: MongoDBMultiCluster`.

## How it works

```
BEFORE                                            AFTER
─────────                                         ─────────
primary OM ─externalApplicationDatabaseRef─►┐     primary OM ──owns──► StatefulSets
 (no spec.applicationDatabase)              │      (spec.applicationDatabase,
                                            ▼       topology: MultiCluster)
 MongoDBMultiCluster(role:AppDB) ─owns─► StatefulSets     MongoDBMultiCluster CR released + deleted
```

1. You start in the external-AppDB state (as in [../01-mc-fresh-start](../01-mc-fresh-start)): a
   management OM, an external `MongoDBMultiCluster` AppDB, and a primary OM referencing it.
2. You patch the primary OM to **remove** `spec.externalApplicationDatabaseRef` and **add back**
   `spec.applicationDatabase` (`topology: MultiCluster`, same members/TLS). The operator asks the
   `MongoDBMultiCluster` CR to **release** the StatefulSets, then **re-adopts** them as an internal
   AppDB. The connection string is identical (same hosts), so the OM pods do not roll.
3. The released CR reports `Pending` ("other owner") and is **deleted**. Its StatefulSets survive and
   are now managed by the OM.

## Prerequisites

This guide starts from a running external-AppDB deployment. Before you begin you must have (all from
[../01-mc-fresh-start](../01-mc-fresh-start)):

- Three clusters (central + two members) with an Istio mesh and the multi-cluster operator installed.
- cert-manager, the TLS CA ConfigMap (`${APPDB_CA_CONFIGMAP}`, including the public roots for
  `downloads.mongodb.com`) and the AppDB server certificate.
- A management OM (`Running`), its API-key secret and the project ConfigMap.
- A primary OM `Running` against an external `MongoDBMultiCluster` AppDB named `<primary-om-name>-db`
  (its `status.applicationDatabase.phase` is `Disabled`).

Set the same variables as in [../01-mc-fresh-start](../01-mc-fresh-start) step 0 (`source
env_variables.sh`).

Quick sanity check of the starting state:

```bash
# primary OM Running with AppDB Disabled (external), and the external AppDB CR Running:
kubectl get om "${PRIMARY_OM_NAME}" --context "${K8S_CTX_0}" -n "${MDB_NS}" \
  -o jsonpath='{.status.opsManager.phase} / appdb={.status.applicationDatabase.phase}{"\n"}'
kubectl get mdbmc "${APPDB_NAME}" --context "${K8S_CTX_0}" -n "${MDB_NS}" \
  -o jsonpath='{.status.phase}{"\n"}'
# -> Running / appdb=Disabled   and   Running
```

---

## 1. Reconfigure the primary OM to an internal multi-cluster AppDB

Remove `spec.externalApplicationDatabaseRef` (set it to `null`) and add back
`spec.applicationDatabase` with `topology: MultiCluster` and the **same member layout and TLS** as the
external AppDB, so the OM can re-adopt the existing multi-cluster StatefulSets.

```bash
kubectl patch om "${PRIMARY_OM_NAME}" \
  --context "${K8S_CTX_0}" -n "${MDB_NS}" \
  --type merge \
  -p "$(cat <<EOF
{
  "spec": {
    "externalApplicationDatabaseRef": null,
    "applicationDatabase": {
      "topology": "MultiCluster",
      "version": "${APPDB_VERSION}",
      "security": {
        "certsSecretPrefix": "${APPDB_CERT_PREFIX}",
        "tls": { "ca": "${APPDB_CA_CONFIGMAP}" }
      },
      "clusterSpecList": [
        { "clusterName": "${K8S_CTX_1}", "members": ${APPDB_MEMBERS_C1} },
        { "clusterName": "${K8S_CTX_2}", "members": ${APPDB_MEMBERS_C2} }
      ]
    }
  }
}
EOF
)"
```

## 2. Wait for release and re-adoption

The `MongoDBMultiCluster` CR releases ownership and reports `Pending` ("other owner"); the OM
re-adopts the StatefulSets as its internal AppDB and goes `Running`.

```bash
kubectl wait --for=jsonpath='{.status.phase}'=Pending \
  mdbmc/"${APPDB_NAME}" --context "${K8S_CTX_0}" -n "${MDB_NS}" --timeout=300s

kubectl get mdbmc "${APPDB_NAME}" --context "${K8S_CTX_0}" -n "${MDB_NS}" \
  -o jsonpath='{.status.message}{"\n"}'

kubectl wait --for=jsonpath='{.status.applicationDatabase.phase}'=Running \
  om/"${PRIMARY_OM_NAME}" --context "${K8S_CTX_0}" -n "${MDB_NS}" --timeout=1200s
kubectl wait --for=jsonpath='{.status.opsManager.phase}'=Running \
  om/"${PRIMARY_OM_NAME}" --context "${K8S_CTX_0}" -n "${MDB_NS}" --timeout=1800s
```

## 3. Delete the released `MongoDBMultiCluster` CR

The handover is complete and the OM owns the StatefulSets. The released CR is a leftover; deleting it
does **not** delete the StatefulSets (it no longer owns them).

```bash
kubectl delete mdbmc "${APPDB_NAME}" \
  --context "${K8S_CTX_0}" -n "${MDB_NS}" --wait=true --timeout=300s
```

## 4. Verify

Each member cluster's AppDB StatefulSet survives with **empty** `ownerReferences` (the deleted CR no
longer owns it), and the OM's internal AppDB is `Running`.

```bash
idx=0
for ctx in "${K8S_CTX_1}" "${K8S_CTX_2}"; do
  sts="${APPDB_NAME}-${idx}"
  echo "--- cluster ${ctx} (StatefulSet ${sts}) ---"
  kubectl get statefulset "${sts}" --context "${ctx}" -n "${MDB_NS}" \
    -o jsonpath='  ownerReferences: {range .metadata.ownerReferences[*]}{.kind}/{.name}{" "}{end}{"\n"}'
  echo "  (empty ownerReferences expected)"
  idx=$((idx + 1))
done

# Primary OM AppDB status must be Running (internally managed again):
kubectl get om "${PRIMARY_OM_NAME}" --context "${K8S_CTX_0}" -n "${MDB_NS}" \
  -o jsonpath='{.status.applicationDatabase.phase}{"\n"}'
```

## Troubleshooting

- **Restored `applicationDatabase` must be `topology: MultiCluster`** with the same member layout and
  TLS as the external AppDB, so the OM can re-adopt the existing multi-cluster StatefulSets.
- **CR stuck `Pending` ("other owner")** — expected after reconfiguring; the CR is meant to be deleted
  (step 3) once the OM has re-adopted the StatefulSets.
- **OM pods roll during the switch** — indicates the computed connection string changed. For the
  default-port replica-set case it is identical; keep the member layout and hosts unchanged.
