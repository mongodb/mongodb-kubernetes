# * MDB_MAX_CONCURRENT_RECONCILES is set in context
# * prepare_operator_deployment sets helm flag with it in operator-installation-config cm before the test is run
# * default_operator fixture is using it, therefore passing that var into helm chart installing the operator
# In the future we should move MDB_MAX_CONCURRENT_RECONCILES into a env var as well and set the operator accordingly
import os
from typing import Optional

import pytest
from kubetester import find_fixture, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from tests.conftest import get_custom_mdb_version


@pytest.fixture(scope="module")
def ops_manager(
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
) -> MongoDBOpsManager:
    # We require using the fixture with maxGroups setting increased.
    # Otherwise, we will run into the limit of 250 per user
    resource = MongoDBOpsManager.from_yaml(find_fixture("om_more_orgs.yaml"), namespace=namespace, name="om")

    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    try_load(resource)
    return resource


def get_replica_set(ops_manager, namespace: str, idx: int) -> MongoDB:
    name = f"mdb-{idx}-rs"
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-perf.yaml"),
        namespace=namespace,
        name=name,
    ).configure(ops_manager)

    replicas = int(os.getenv("PERF_TASK_REPLICAS", "3"))
    resource["spec"]["members"] = replicas
    resource.set_version(get_custom_mdb_version())

    try_load(resource)
    return resource


def get_all_rs(ops_manager, namespace) -> list[MongoDB]:
    deployments = int(os.getenv("PERF_TASK_DEPLOYMENTS", "100"))
    return [get_replica_set(ops_manager, namespace, idx) for idx in range(0, deployments)]


@pytest.mark.e2e_om_reconcile_perf
def test_create_om(ops_manager: MongoDBOpsManager):
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)


@pytest.mark.e2e_om_reconcile_perf
def test_create_mdb(ops_manager, namespace: str):
    for resource in get_all_rs(ops_manager, namespace):
        resource["spec"]["security"] = {
            "authentication": {"agents": {"mode": "SCRAM"}, "enabled": True, "modes": ["SCRAM"]}
        }
        resource.set_version(get_custom_mdb_version())
        resource.update()

    for r in get_all_rs(ops_manager, namespace):
        r.assert_reaches_phase(Phase.Running, timeout=2000)


@pytest.mark.e2e_om_reconcile_perf
def test_update_mdb(ops_manager, namespace: str):
    for resource in get_all_rs(ops_manager, namespace):
        additional_mongod_config = {
            "auditLog": {
                "destination": "file",
                "format": "JSON",
                "path": "/var/log/mongodb-mms-automation/mongodb-audit-changed.log",
            }
        }
        resource["spec"]["additionalMongodConfig"] = additional_mongod_config
        resource.update()

    for r in get_all_rs(ops_manager, namespace):
        r.assert_reaches_phase(Phase.Running, timeout=2000)
