from typing import Optional

import yaml
from kubetester.kubetester import (
    fixture as yaml_fixture,
    skip_if_local,
    KubernetesTester,
)
from kubetester import get_default_storage_class
from kubetester.mongodb import Phase, MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark

BUNDLED_APP_DB_VERSION = "4.2.2-ent"
# we can use the custom_mdb_version fixture when we release mongodb-enterprise-init-mongod-rhel and
# mongodb-enterprise-init-mongod-ubuntu1604 for 4.4+ versions, so far let's use the constant
VERSION_IN_OPS_MANAGER = "4.2.8-ent"
VERSION_NOT_IN_OPS_MANAGER = "4.2.1"


@fixture(scope="module")
def ops_manager(
    namespace: str, custom_version: Optional[str], custom_appdb_version: str
) -> MongoDBOpsManager:
    with open(yaml_fixture("mongodb_versions_claim.yaml"), "r") as f:
        pvc_body = yaml.safe_load(f.read())

    KubernetesTester.create_pvc(
        namespace, body=pvc_body, storage_class_name=get_default_storage_class()
    )

    """ The fixture for Ops Manager to be created."""
    om: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_localmode-single-pv.yaml"), namespace=namespace
    )
    om.set_version(custom_version)
    om.set_appdb_version(custom_appdb_version)
    yield om.create()

    KubernetesTester.delete_pvc(namespace, "mongodb-versions-claim")


@fixture(scope="module")
def replica_set(ops_manager: MongoDBOpsManager, namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"), namespace=namespace,
    ).configure(ops_manager, "my-replica-set")
    resource["spec"]["version"] = VERSION_IN_OPS_MANAGER
    yield resource.create()


@mark.e2e_om_localmode
def test_ops_manager_reaches_running_phase(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=100)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_om_localmode
def test_replica_set_reaches_running_phase(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_om_localmode
def test_replica_set_version_upgraded_reaches_failed_phase(replica_set: MongoDB):
    replica_set["spec"]["version"] = VERSION_NOT_IN_OPS_MANAGER
    replica_set.update()
    replica_set.assert_reaches_phase(
        Phase.Failed,
        msg_regexp=f".*Invalid config: MongoDB version {VERSION_NOT_IN_OPS_MANAGER} is not available.*",
    )


@mark.e2e_om_localmode
def test_replica_set_recovers(replica_set: MongoDB):
    replica_set["spec"]["version"] = VERSION_IN_OPS_MANAGER
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)


@skip_if_local
@mark.e2e_om_localmode
def test_client_can_connect_to_mongodb(replica_set: MongoDB):
    replica_set.assert_connectivity()


@mark.e2e_om_localmode
def test_restart_ops_manager_pod(ops_manager: MongoDBOpsManager):
    ops_manager.load()
    ops_manager["spec"]["configuration"]["mms.testUtil.enabled"] = "false"
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_om_localmode
def test_can_scale_replica_set(replica_set: MongoDB):
    replica_set.load()
    replica_set["spec"]["members"] = 5
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)


@skip_if_local
@mark.e2e_om_localmode
def test_client_can_still_connect(replica_set: MongoDB):
    replica_set.assert_connectivity()
