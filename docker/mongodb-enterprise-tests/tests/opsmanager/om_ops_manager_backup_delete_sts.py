from typing import Optional

from kubetester import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.kubetester import fixture as yaml_fixture

from kubetester.mongodb import Phase
from pytest import mark, fixture
from tests.opsmanager.om_ops_manager_backup import (
    OPLOG_RS_NAME,
    BLOCKSTORE_RS_NAME,
)

DEFAULT_APPDB_USER_NAME = "mongodb-ops-manager"


@fixture(scope="module")
def oplog_replica_set(ops_manager, namespace) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=OPLOG_RS_NAME,
    ).configure(ops_manager, "development")

    return resource.create()


@fixture(scope="module")
def ops_manager(
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_backup_delete_sts.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    return resource.create()


@fixture(scope="module")
def blockstore_replica_set(
    ops_manager,
) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=ops_manager.namespace,
        name=BLOCKSTORE_RS_NAME,
    ).configure(ops_manager, "blockstore")

    return resource.create()


@mark.e2e_om_ops_manager_backup_delete_sts
def test_create_om(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_om_ops_manager_backup_delete_sts
def test_create_backing_replica_sets(
    oplog_replica_set: MongoDB, blockstore_replica_set: MongoDB
):
    oplog_replica_set.assert_reaches_phase(Phase.Running)
    blockstore_replica_set.assert_reaches_phase(Phase.Running)


@mark.e2e_om_ops_manager_backup_delete_sts
def test_backup_statefulset_gets_recreated(
    ops_manager: MongoDBOpsManager,
):
    # Wait for the the backup to be fully running
    ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=200)
    ops_manager.load()
    ops_manager["spec"]["backup"]["statefulSet"] = {
        "spec": {"revisionHistoryLimit": 15}
    }
    ops_manager.update()

    ops_manager.backup_status().assert_abandons_phase(Phase.Running, timeout=200)

    ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=200)
    # the backup statefulset should have been recreated
    ops_manager.read_backup_statefulset()
