# External AppDB Switch — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Write a Python e2e test (`om_external_appdb.py`) that deploys two Ops Managers, switches the Primary OM from an internal AppDB to an external one via `externalApplicationDatabaseRef`, and verifies the Primary OM remains healthy throughout.

**Architecture:** Two `MongoDBOpsManager` CRs (`meta-om`, `primary-om`) in the same namespace. After the Primary OM stabilises with a standard internal AppDB, a dedicated secret is created from the internal connection string and the Primary OM is patched to use `externalApplicationDatabaseRef`. A `MongoDB` resource named identically to the AppDB StatefulSet is then created against Meta OM, taking over the StatefulSet while leaving PVCs intact.

**Tech Stack:** Python 3, pytest, `kubetester` (project-internal helpers), kubernetes-client, `MongoDBOpsManager`/`MongoDB` custom objects.

---

## File Map

| Action | Path |
|---|---|
| Create | `docker/mongodb-kubernetes-tests/tests/opsmanager/fixtures/om_external_appdb_meta.yaml` |
| Create | `docker/mongodb-kubernetes-tests/tests/opsmanager/fixtures/om_external_appdb_primary.yaml` |
| Modify | `docker/mongodb-kubernetes-tests/kubetester/opsmanager.py` — add `set_external_appdb_ref` after line 843 |
| Create | `docker/mongodb-kubernetes-tests/tests/opsmanager/om_external_appdb.py` |

---

## Task 1: Create Ops Manager fixture YAMLs

**Files:**
- Create: `docker/mongodb-kubernetes-tests/tests/opsmanager/fixtures/om_external_appdb_meta.yaml`
- Create: `docker/mongodb-kubernetes-tests/tests/opsmanager/fixtures/om_external_appdb_primary.yaml`

- [ ] **Step 1: Create `om_external_appdb_meta.yaml`**

```yaml
# docker/mongodb-kubernetes-tests/tests/opsmanager/fixtures/om_external_appdb_meta.yaml
apiVersion: mongodb.com/v1
kind: MongoDBOpsManager
metadata:
  name: meta-om
spec:
  replicas: 1
  version: 6.0.0
  adminCredentials: ops-manager-admin-secret

  applicationDatabase:
    members: 3
    version: 6.0.20-ent

  backup:
    enabled: false

  configuration:
    automation.versions.source: mongodb
    mms.adminEmailAddr: cloud-manager-support@mongodb.com
    mms.fromEmailAddr: cloud-manager-support@mongodb.com
    mms.ignoreInitialUiSetup: "true"
    mms.mail.hostname: email-smtp.us-east-1.amazonaws.com
    mms.mail.port: "465"
    mms.mail.ssl: "true"
    mms.mail.transport: smtp
    mms.minimumTLSVersion: TLSv1.2
    mms.replyToEmailAddr: cloud-manager-support@mongodb.com
```

- [ ] **Step 2: Create `om_external_appdb_primary.yaml`**

```yaml
# docker/mongodb-kubernetes-tests/tests/opsmanager/fixtures/om_external_appdb_primary_om.yaml
apiVersion: mongodb.com/v1
kind: MongoDBOpsManager
metadata:
  name: primary-om
spec:
  replicas: 1
  version: 6.0.0
  adminCredentials: ops-manager-admin-secret

  applicationDatabase:
    members: 3
    version: 6.0.20-ent

  backup:
    enabled: false

  configuration:
    automation.versions.source: mongodb
    mms.adminEmailAddr: cloud-manager-support@mongodb.com
    mms.fromEmailAddr: cloud-manager-support@mongodb.com
    mms.ignoreInitialUiSetup: "true"
    mms.mail.hostname: email-smtp.us-east-1.amazonaws.com
    mms.mail.port: "465"
    mms.mail.ssl: "true"
    mms.mail.transport: smtp
    mms.minimumTLSVersion: TLSv1.2
    mms.replyToEmailAddr: cloud-manager-support@mongodb.com
```

> Note: `version` and `applicationDatabase.version` will be overridden at test runtime by `set_version()` / `set_appdb_version()`. The values in the YAML just need to be syntactically valid.

- [ ] **Step 3: Commit**

```bash
git add docker/mongodb-kubernetes-tests/tests/opsmanager/fixtures/om_external_appdb_meta.yaml \
        docker/mongodb-kubernetes-tests/tests/opsmanager/fixtures/om_external_appdb_primary_om.yaml
git commit -m "test(e2e): add fixture YAMLs for external appDB switch test"
```

---

## Task 2: Add `set_external_appdb_ref` to MongoDBOpsManager

**Files:**
- Modify: `docker/mongodb-kubernetes-tests/kubetester/opsmanager.py` (after line 843, after `set_appdb_version`)

The existing `set_appdb_version` method at line 843 looks like:
```python
def set_appdb_version(self, version: str):
    self["spec"]["applicationDatabase"]["version"] = version
```

- [ ] **Step 1: Add the helper method immediately after `set_appdb_version`**

Open `docker/mongodb-kubernetes-tests/kubetester/opsmanager.py` and insert after `set_appdb_version`:

```python
def set_external_appdb_ref(self, secret_name: str, secret_key: str = "connectionString.standard") -> None:
    """Configures externalApplicationDatabaseRef so the OM reads its AppDB connection string from a secret."""
    self["spec"]["externalApplicationDatabaseRef"] = {
        "connectionStringSecretRef": {
            "name": secret_name,
            "key": secret_key,
        }
    }
```

- [ ] **Step 2: Verify the file parses cleanly**

```bash
cd docker/mongodb-kubernetes-tests
python -c "from kubetester.opsmanager import MongoDBOpsManager; print('OK')"
```

Expected output: `OK`

- [ ] **Step 3: Commit**

```bash
git add docker/mongodb-kubernetes-tests/kubetester/opsmanager.py
git commit -m "feat(kubetester): add set_external_appdb_ref helper to MongoDBOpsManager"
```

---

## Task 3: Create the test file — imports, constants, and fixtures

**Files:**
- Create: `docker/mongodb-kubernetes-tests/tests/opsmanager/om_external_appdb.py`

- [ ] **Step 1: Create the file with imports, constants, and all four module-scoped fixtures**

```python
# docker/mongodb-kubernetes-tests/tests/opsmanager/om_external_appdb.py
from typing import Iterator

import pytest
from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests import test_logger

logger = test_logger.get_test_logger(__name__)

EXT_APPDB_SECRET_NAME = "primary-om-db-ext-connection-string"
EXT_APPDB_SECRET_KEY = "connectionString.standard"


@fixture(scope="module")
def meta_om(namespace: str, custom_version: str, custom_appdb_version: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_external_appdb_meta.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    try_load(resource)
    return resource


@fixture(scope="module")
def primary_om(namespace: str, custom_version: str, custom_appdb_version: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_external_appdb_primary_om.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    try_load(resource)
    return resource


@fixture(scope="module")
def primary_mdb(primary_om: MongoDBOpsManager, namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name="canary-rs",
    )
    resource.configure(primary_om, "development")
    try_load(resource)
    return resource


@fixture(scope="module")
def primary_om_external_appdb(meta_om: MongoDBOpsManager, primary_om: MongoDBOpsManager, namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=primary_om.app_db_name(),  # "primary-om-db" — same name as the AppDB StatefulSet
    )
    resource.configure(meta_om, "appdb-project")
    # Match the version already running in the AppDB StatefulSet to avoid an immediate upgrade on takeover.
    resource.set_version(primary_om["spec"]["applicationDatabase"]["version"])
    try_load(resource)
    return resource
```

- [ ] **Step 2: Verify the imports resolve**

```bash
cd docker/mongodb-kubernetes-tests
python -c "import tests.opsmanager.om_external_appdb; print('OK')"
```

Expected output: `OK`

- [ ] **Step 3: Commit**

```bash
git add docker/mongodb-kubernetes-tests/tests/opsmanager/om_external_appdb.py
git commit -m "test(e2e): scaffold om_external_appdb test — imports, constants, fixtures"
```

---

## Task 4: Add TestSetup and TestPreSwitchCanary

**Files:**
- Modify: `docker/mongodb-kubernetes-tests/tests/opsmanager/om_external_appdb.py`

Append the following two classes to the bottom of `om_external_appdb.py`.

- [ ] **Step 1: Append `TestSetup`**

```python
@pytest.mark.e2e_om_external_appdb
class TestSetup:
    def test_meta_om_created(self, meta_om: MongoDBOpsManager):
        meta_om.update()
        meta_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
        meta_om.om_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_primary_om_created(self, primary_om: MongoDBOpsManager):
        primary_om.update()
        primary_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=600)
```

- [ ] **Step 2: Append `TestPreSwitchCanary`**

```python
@pytest.mark.e2e_om_external_appdb
class TestPreSwitchCanary:
    def test_primary_mdb_created(self, primary_mdb: MongoDB):
        primary_mdb.update()
        primary_mdb.assert_reaches_phase(Phase.Running, timeout=600)

    def test_primary_mdb_connectivity(self, primary_mdb: MongoDB):
        primary_mdb.assert_connectivity()
```

- [ ] **Step 3: Commit**

```bash
git add docker/mongodb-kubernetes-tests/tests/opsmanager/om_external_appdb.py
git commit -m "test(e2e): add TestSetup and TestPreSwitchCanary to external appDB test"
```

---

## Task 5: Add TestSwitchToExternalAppDB and TestPostSwitchVerification

**Files:**
- Modify: `docker/mongodb-kubernetes-tests/tests/opsmanager/om_external_appdb.py`

Append the following two classes.

- [ ] **Step 1: Append `TestSwitchToExternalAppDB`**

```python
@pytest.mark.e2e_om_external_appdb
class TestSwitchToExternalAppDB:
    def test_create_external_appdb_secret(self, primary_om: MongoDBOpsManager, namespace: str):
        """Copy the internal AppDB connection string into a dedicated secret with the standard key."""
        connection_string = primary_om.read_appdb_connection_url()
        create_or_update_secret(
            namespace=namespace,
            name=EXT_APPDB_SECRET_NAME,
            data={EXT_APPDB_SECRET_KEY: connection_string},
        )

    def test_switch_primary_om_to_external_appdb(self, primary_om: MongoDBOpsManager):
        """Patch Primary OM to use externalApplicationDatabaseRef; AppDB reconciliation is then skipped."""
        primary_om.load()
        primary_om.set_external_appdb_ref(EXT_APPDB_SECRET_NAME, EXT_APPDB_SECRET_KEY)
        primary_om.update()
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_primary_mdb_still_running_after_switch(self, primary_mdb: MongoDB):
        primary_mdb.reload()
        primary_mdb.assert_reaches_phase(Phase.Running, timeout=300)
```

- [ ] **Step 2: Append `TestPostSwitchVerification`**

```python
@pytest.mark.e2e_om_external_appdb
class TestPostSwitchVerification:
    def test_primary_om_healthy(self, primary_om: MongoDBOpsManager):
        primary_om.get_om_tester().assert_healthiness()

    def test_primary_mdb_connectivity(self, primary_mdb: MongoDB):
        primary_mdb.assert_connectivity()
```

- [ ] **Step 3: Commit**

```bash
git add docker/mongodb-kubernetes-tests/tests/opsmanager/om_external_appdb.py
git commit -m "test(e2e): add TestSwitchToExternalAppDB and TestPostSwitchVerification"
```

---

## Task 6: Add TestAppDBTakeover and TestFinalVerification

**Files:**
- Modify: `docker/mongodb-kubernetes-tests/tests/opsmanager/om_external_appdb.py`

Append the final two classes.

- [ ] **Step 1: Append `TestAppDBTakeover`**

```python
@pytest.mark.e2e_om_external_appdb
class TestAppDBTakeover:
    def test_primary_om_external_appdb_created(self, primary_om_external_appdb: MongoDB):
        """
        Submit the MongoDB resource named 'primary-om-db' against Meta OM.
        The MongoDB controller reconciles the existing AppDB StatefulSet in-place;
        PVCs are re-mounted to the new pods without being deleted or recreated.
        """
        primary_om_external_appdb.update()
        primary_om_external_appdb.assert_reaches_phase(Phase.Running, timeout=900)

    def test_primary_om_external_appdb_connectivity(self, primary_om_external_appdb: MongoDB):
        primary_om_external_appdb.assert_connectivity()
```

- [ ] **Step 2: Append `TestFinalVerification`**

```python
@pytest.mark.e2e_om_external_appdb
class TestFinalVerification:
    def test_primary_om_still_healthy(self, primary_om: MongoDBOpsManager):
        """Primary OM must still be reachable after the MongoDB controller took over the StatefulSet."""
        primary_om.get_om_tester().assert_healthiness()

    def test_primary_mdb_still_running(self, primary_mdb: MongoDB):
        primary_mdb.reload()
        primary_mdb.assert_reaches_phase(Phase.Running, timeout=300)
        primary_mdb.assert_connectivity()

    def test_primary_om_external_appdb_still_running(self, primary_om_external_appdb: MongoDB):
        primary_om_external_appdb.reload()
        primary_om_external_appdb.assert_reaches_phase(Phase.Running, timeout=300)
```

- [ ] **Step 3: Commit**

```bash
git add docker/mongodb-kubernetes-tests/tests/opsmanager/om_external_appdb.py
git commit -m "test(e2e): add TestAppDBTakeover and TestFinalVerification — completes external appDB switch test"
```

---

## Self-review notes

- `read_appdb_connection_url()` reads the secret `<om-name>-db-connection-string` with key `connectionString` (not `connectionString.standard`). The dedicated secret we create in `test_create_external_appdb_secret` maps this value to key `connectionString.standard`, matching the default the controller expects.
- `primary_om_external_appdb.name == primary_om.app_db_name()` → `"primary-om-db"`. The MongoDB controller will find the existing StatefulSet with this name and reconcile it rather than creating a fresh one.
- `try_load` in the `primary_om_external_appdb` fixture is safe — if the resource doesn't exist yet the call returns `False` and is a no-op. The actual creation happens in `TestAppDBTakeover.test_primary_om_external_appdb_created`.
- `set_version` on `MongoDBOpsManager` is a no-op when `version` is `None` (guarded internally), so `custom_version: Optional[str]` fixture from conftest is compatible.
