import time
from typing import Optional

import pytest
from kubernetes import client
from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests.conftest import get_central_cluster_client, is_multi_cluster
from tests.opsmanager.om_ops_manager_backup import BLOCKSTORE_RS_NAME, OPLOG_RS_NAME
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

MONGO_URI_VOLUME_MOUNT_NAME = "mongodb-uri"
MONGO_URI_VOLUME_MOUNT_PATH = "/mongodb-ops-manager/.mongodb-mms-connection-string"


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(yaml_fixture("om_ops_manager_secure_config.yaml"), namespace=namespace)

    if try_load(resource):
        return resource

    create_or_update_secret(
        namespace,
        "my-password",
        {"password": "password"},
        api_client=get_central_cluster_client(),
    )

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    resource["spec"]["applicationDatabase"]["passwordSecretKeyRef"] = {
        "name": "my-password",
    }
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    resource.update()

    return resource


@fixture(scope="module")
def oplog_replica_set(ops_manager, namespace, custom_mdb_version) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=OPLOG_RS_NAME,
    ).configure(ops_manager)

    resource.set_version(custom_mdb_version)

    try_load(resource)
    return resource


@fixture(scope="module")
def blockstore_replica_set(ops_manager, custom_mdb_version) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=ops_manager.namespace,
        name=BLOCKSTORE_RS_NAME,
    ).configure(ops_manager)

    resource.set_version(custom_mdb_version)

    try_load(resource)
    return resource


@pytest.mark.e2e_om_ops_manager_secure_config
def test_om_creation(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_om_ops_manager_secure_config
def test_appdb_monitoring_configured(ops_manager: MongoDBOpsManager):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager.assert_appdb_monitoring_group_was_created()


@pytest.mark.e2e_om_ops_manager_secure_config
def test_backing_dbs_created(oplog_replica_set: MongoDB, blockstore_replica_set: MongoDB):
    oplog_replica_set.update()
    blockstore_replica_set.update()

    oplog_replica_set.assert_reaches_phase(Phase.Running)
    blockstore_replica_set.assert_reaches_phase(Phase.Running)


@pytest.mark.e2e_om_ops_manager_secure_config
def test_backup_enabled(ops_manager: MongoDBOpsManager):
    ops_manager.load()
    ops_manager["spec"]["backup"]["enabled"] = True

    if is_multi_cluster():
        enable_multi_cluster_deployment(ops_manager)

    ops_manager.update()
    ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_om_ops_manager_secure_config
def test_connection_string_secret_was_created(ops_manager: MongoDBOpsManager):
    secret_data = KubernetesTester.read_secret(
        ops_manager.namespace, ops_manager.get_appdb_connection_url_secret_name()
    )
    assert "connectionString" in secret_data


@pytest.mark.e2e_om_ops_manager_secure_config
def test_ops_manager_pod_template_was_annotated(ops_manager: MongoDBOpsManager):
    sts = ops_manager.read_statefulset()
    pod_template = sts.spec.template
    assert "connectionStringHash" in pod_template.metadata.annotations
    assert pod_template.metadata.annotations["connectionStringHash"] != ""


@pytest.mark.e2e_om_ops_manager_secure_config
def test_backup_pod_template_was_annotated(ops_manager: MongoDBOpsManager):
    sts = ops_manager.read_backup_statefulset()
    pod_template = sts.spec.template
    assert "connectionStringHash" in pod_template.metadata.annotations
    assert pod_template.metadata.annotations["connectionStringHash"] != ""


@pytest.mark.e2e_om_ops_manager_secure_config
def test_connection_string_is_configured_securely(ops_manager: MongoDBOpsManager):
    sts = ops_manager.read_statefulset()
    om_container = sts.spec.template.spec.containers[0]
    volume_mounts: list[client.V1VolumeMount] = om_container.volume_mounts

    connection_string_volume_mount = [vm for vm in volume_mounts if vm.name == MONGO_URI_VOLUME_MOUNT_NAME][0]
    assert connection_string_volume_mount.mount_path == MONGO_URI_VOLUME_MOUNT_PATH


@pytest.mark.e2e_om_ops_manager_secure_config
def test_changing_app_db_password_triggers_rolling_restart(
    ops_manager: MongoDBOpsManager,
):
    KubernetesTester.update_secret(
        ops_manager.namespace,
        "my-password",
        {
            "password": "new-password",
        },
    )
    # unfortunately changing the external secret doesn't change the metadata.generation so we cannot track
    # when the Operator started working on the spec - let's just wait for a bit
    time.sleep(5)
    ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=100)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_om_ops_manager_secure_config
def test_no_unnecessary_rolling_upgrades_happen(
    ops_manager: MongoDBOpsManager,
    custom_appdb_version: str,
):
    sts = ops_manager.read_statefulset()
    old_generation = sts.metadata.generation
    old_hash = sts.spec.template.metadata.annotations["connectionStringHash"]

    backup_sts = ops_manager.read_backup_statefulset()
    old_backup_generation = backup_sts.metadata.generation
    old_backup_hash = backup_sts.spec.template.metadata.annotations["connectionStringHash"]

    assert old_backup_hash == old_hash

    ops_manager.load()
    ops_manager["spec"]["applicationDatabase"]["version"] = custom_appdb_version
    ops_manager.update()

    time.sleep(10)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    sts = ops_manager.read_statefulset()
    assert sts.metadata.generation == old_generation, "no change should have happened to the Ops Manager stateful set"
    assert (
        sts.spec.template.metadata.annotations["connectionStringHash"] == old_hash
    ), "connection string hash should have remained the same"

    backup_sts = ops_manager.read_backup_statefulset()
    assert (
        backup_sts.metadata.generation == old_backup_generation
    ), "no change should have happened to the backup stateful set"
    assert (
        backup_sts.spec.template.metadata.annotations["connectionStringHash"] == old_backup_hash
    ), "connection string hash should have remained the same"


@pytest.mark.e2e_om_ops_manager_secure_config
def test_connection_string_secret_was_updated(ops_manager: MongoDBOpsManager):
    connection_string = ops_manager.read_appdb_connection_url()

    assert "new-password" in connection_string


@pytest.mark.e2e_om_ops_manager_secure_config
def test_appdb_project_has_correct_auth_tls(ops_manager: MongoDBOpsManager):
    automation_config_tester = ops_manager.get_automation_config_tester()
    auth = automation_config_tester.automation_config["auth"]

    automation_config_tester.assert_agent_user("mms-automation-agent")
    assert auth["keyfile"] == "/var/lib/mongodb-mms-automation/authentication/keyfile"

    # These should have been set to random values
    assert auth["autoPwd"] != ""
    assert auth["key"] != ""
