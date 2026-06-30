# External AppDB Switch — e2e Test Design

**Date:** 2026-06-30  
**Branch:** `maciejk/external-appdb`  
**Marker:** `e2e_om_external_appdb`

---

## Goal

Manual/automated e2e test that validates the `externalApplicationDatabaseRef` feature: a Primary Ops Manager stops reconciling its internal AppDB and connects instead to an external MongoDB replica set whose StatefulSet (and PVCs) is the same physical one that used to be the internal AppDB.

---

## Topology

Both Ops Manager instances deploy in the **same namespace**.

| Resource | Name | Role |
|---|---|---|
| Meta OM | `meta-om` | Management plane; owns the project that manages the AppDB-takeover MongoDB resource |
| Primary OM | `primary-om` | The OM under test; starts with internal AppDB, switches to external |
| Primary OM AppDB | `primary-om-db` | Internal AppDB StatefulSet; becomes externally managed after switch |
| `primary_mdb` | `canary-rs` | MongoDB RS configured against Primary OM; canary for Primary OM health throughout |
| `primary_om_external_appdb` | `primary-om-db` | MongoDB RS configured against Meta OM; takes over the existing AppDB StatefulSet |
| External AppDB secret | `primary-om-db-ext-connection-string` | Dedicated secret created from the internal connection string, key `connectionString.standard` |

---

## Test Sequence

### Phase 1 — `TestSetup`
1. Deploy Meta OM (`meta-om`) with its own internal AppDB.
2. Deploy Primary OM (`primary-om`) with standard internal AppDB (`primary-om-db`, 3 members).
3. Assert both OMs reach `Phase.Running`.

### Phase 2 — `TestPreSwitchCanary`
1. Create `canary-rs` (`primary_mdb`) as a MongoDB replica set configured against Primary OM in project `"development"`.
2. Assert `primary_mdb` reaches `Phase.Running`.
3. Assert `primary_mdb` connectivity (`assert_connectivity()`).

### Phase 3 — `TestSwitchToExternalAppDB`
1. Read secret `primary-om-db-connection-string` (key: `connectionString`) — the internal AppDB connection string written by the OM AppDB reconciler.
2. Write that value into a new secret `primary-om-db-ext-connection-string` under key `connectionString.standard`.
3. Patch Primary OM spec to add:
   ```yaml
   externalApplicationDatabaseRef:
     connectionStringSecretRef:
       name: primary-om-db-ext-connection-string
       key: connectionString.standard
   ```
4. Assert Primary OM reaches `Phase.Running` again (AppDB reconciliation is now skipped).
5. Assert `primary_mdb` is still `Phase.Running` (Primary OM still serves).

### Phase 4 — `TestPostSwitchVerification`
1. `primary_om.get_om_tester().assert_healthiness()` — Primary OM HTTP endpoint is up.
2. Assert `primary_mdb` still `Phase.Running` and connectivity holds.

### Phase 5 — `TestAppDBTakeover`
1. Create a `MongoDB` resource named `primary-om-db` (same name as the AppDB StatefulSet) configured against Meta OM in project `"appdb-project"`.
   - MongoDB version matches Primary OM's AppDB version so the controller does not trigger an immediate version upgrade.
2. Assert `primary_om_external_appdb` reaches `Phase.Running` (MongoDB controller reconciles existing StatefulSet; PVCs survive and are re-mounted).

### Phase 6 — `TestFinalVerification`
1. `primary_om.get_om_tester().assert_healthiness()` — Primary OM still healthy after MongoDB controller took over the StatefulSet.
2. Assert `primary_mdb` still `Phase.Running` and connectivity holds.
3. Assert `primary_om_external_appdb` still `Phase.Running`.

---

## Files

| File | Action |
|---|---|
| `tests/opsmanager/om_external_appdb.py` | New — test logic |
| `tests/opsmanager/fixtures/om_external_appdb_meta.yaml` | New — Meta OM CR |
| `tests/opsmanager/fixtures/om_external_appdb_primary.yaml` | New — Primary OM CR |
| `tests/opsmanager/fixtures/replica-set-for-om.yaml` | Reused — for both `primary_mdb` and `primary_om_external_appdb` |

---

## Fixtures

```python
@fixture(scope="module")
def meta_om(namespace, custom_version, custom_appdb_version) -> MongoDBOpsManager:
    # loads om_external_appdb_meta.yaml
    # name: meta-om

@fixture(scope="module")
def primary_om(namespace, custom_version, custom_appdb_version) -> MongoDBOpsManager:
    # loads om_external_appdb_primary.yaml
    # name: primary-om, applicationDatabase.members: 3

@fixture(scope="module")
def primary_mdb(primary_om, namespace) -> MongoDB:
    # replica-set-for-om.yaml, name="canary-rs"
    # configure(primary_om, "development")

@fixture(scope="module")
def primary_om_external_appdb(meta_om, primary_om, namespace) -> MongoDB:
    # replica-set-for-om.yaml, name=primary_om.app_db_name()  ("primary-om-db")
    # configure(meta_om, "appdb-project")
    # version = primary_om's appdb version (no upgrade on takeover)
```

---

## Key Implementation Notes

- **Secret copy** is done inside `TestSwitchToExternalAppDB` using `create_or_update_secret()`, not in a fixture, so it is part of the readable test narrative.
- `primary_om_external_appdb.name` must equal `primary_om.app_db_name()` so the MongoDB controller reconciles the *existing* StatefulSet rather than creating a new one. PVCs survive because they are bound to pod names, not the controller that manages the StatefulSet.
- The `primary_om_external_appdb` fixture is defined at module scope but its `.update()` call (which submits it to the cluster) lives inside `TestAppDBTakeover`, matching the pattern used in `om_ops_manager_upgrade.py` for deferred resource creation.
- `conftest.py` `pytest_runtest_setup` injects `default_operator`; no override needed.

---

## Success Criteria

- Primary OM is `Running` after the switch and responds to health checks.
- `primary_mdb` (canary RS against Primary OM) is `Running` and connected at every phase boundary.
- `primary_om_external_appdb` reaches `Running` under Meta OM's control.
- No test step requires deleting or recreating any PVC.
