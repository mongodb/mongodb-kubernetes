from typing import Optional

import yaml
from kubetester import get_default_storage_class, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import get_member_cluster_api_client, is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment, get_om_member_cluster_names

# This version is not supported by the Ops Manager and is not present in the local mode.
VERSION_NOT_IN_OPS_MANAGER = "6.0.4"


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    with open(yaml_fixture("mongodb_versions_claim.yaml"), "r") as f:
        pvc_body = yaml.safe_load(f.read())

    """ The fixture for Ops Manager to be created."""
    om: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_localmode-single-pv.yaml"), namespace=namespace
    )
    om.set_version(custom_version)
    om.set_appdb_version(custom_appdb_version)
    if is_multi_cluster():
        enable_multi_cluster_deployment(om)
        for member_cluster_name in get_om_member_cluster_names():
            member_client = get_member_cluster_api_client(member_cluster_name=member_cluster_name)
            KubernetesTester.create_or_update_pvc(
                namespace,
                body=pvc_body,
                storage_class_name=get_default_storage_class(),
                api_client=member_client,
            )
    else:
        KubernetesTester.create_or_update_pvc(namespace, body=pvc_body, storage_class_name=get_default_storage_class())

    try_load(om)
    return om


@fixture(scope="module")
def replica_set(ops_manager: MongoDBOpsManager, namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
    ).configure(ops_manager, "my-replica-set")
    resource["spec"]["version"] = custom_mdb_version
    try_load(resource)
    return resource


@mark.e2e_om_localmode
def test_ops_manager_reaches_running_phase(ops_manager: MongoDBOpsManager):
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=800)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)


# Since this is running OM in local mode, and OM6 is EOL, the latest mongodb versions are not available, unless we manually update the version manifest
@mark.e2e_om_localmode
def test_update_om_version_manifest(ops_manager: MongoDBOpsManager):
    ops_manager.update_version_manifest()


@mark.e2e_om_localmode
def test_replica_set_reaches_running_phase(replica_set: MongoDB):
    replica_set.update()
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
def test_replica_set_recovers(replica_set: MongoDB, custom_mdb_version: str):
    replica_set["spec"]["version"] = custom_mdb_version
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
    # We should retry if we are running into errors while adding new members.
    replica_set.assert_reaches_phase(Phase.Running, timeout=600, ignore_errors=True)


@skip_if_local
@mark.e2e_om_localmode
def test_client_can_still_connect(replica_set: MongoDB):
    replica_set.assert_connectivity()
