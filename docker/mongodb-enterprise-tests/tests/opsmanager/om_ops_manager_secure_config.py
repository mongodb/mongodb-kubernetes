import pytest
from pytest import fixture
from tests.opsmanager.om_ops_manager_backup import BLOCKSTORE_RS_NAME, OPLOG_RS_NAME

from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.opsmanager import MongoDBOpsManager

MONGO_URI_ENV_NAME = "OM_PROP_mongo_mongoUri"


@fixture(scope="module")
def ops_manager(namespace: str) -> MongoDBOpsManager:
    KubernetesTester.create_secret(namespace, "my-password", {"password": "password"})

    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_secure_config.yaml"), namespace=namespace
    )

    resource["spec"]["applicationDatabase"]["passwordSecretKeyRef"] = {
        "name": "my-password",
    }

    return resource.create()


@fixture(scope="module")
def oplog_replica_set(ops_manager, namespace) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=OPLOG_RS_NAME,
    ).configure(ops_manager, "oplog")
    return resource.create()


@fixture(scope="module")
def blockstore_replica_set(ops_manager) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=ops_manager.namespace,
        name=BLOCKSTORE_RS_NAME,
    ).configure(ops_manager, "blockstore")

    return resource.create()


@pytest.mark.e2e_om_ops_manager_secure_config
def test_om_creation(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_om_ops_manager_secure_config
def test_appdb_monitoring_configured(ops_manager: MongoDBOpsManager):
    ops_manager.appdb_status().assert_abandons_phase(Phase.Running)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager.assert_appdb_monitoring_group_was_created()


@pytest.mark.e2e_om_ops_manager_secure_config
def test_backing_dbs_created(
    oplog_replica_set: MongoDB, blockstore_replica_set: MongoDB
):
    oplog_replica_set.assert_reaches_phase(Phase.Running)
    blockstore_replica_set.assert_reaches_phase(Phase.Running)


@pytest.mark.e2e_om_ops_manager_secure_config
def test_backup_enabled(ops_manager: MongoDBOpsManager):
    ops_manager.load()
    ops_manager["spec"]["backup"]["enabled"] = True
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
    connection_string_env = [
        env for env in om_container.env if env.name == MONGO_URI_ENV_NAME
    ][0]
    assert (
        connection_string_env.value_from.secret_key_ref.name
        == ops_manager.get_appdb_connection_url_secret_name()
    )
    assert connection_string_env.value_from.secret_key_ref.key == "connectionString"


@pytest.mark.e2e_om_ops_manager_secure_config
def test_changing_app_db_password_triggers_rolling_restart(
    ops_manager: MongoDBOpsManager,
):
    KubernetesTester.update_secret(
        ops_manager.namespace, "my-password", {"password": "new-password",},
    )
    ops_manager.om_status().assert_abandons_phase(phase=Phase.Running)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    ops_manager.backup_status().assert_abandons_phase(phase=Phase.Running)
    ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=200)


@pytest.mark.e2e_om_ops_manager_secure_config
def test_no_unnecessary_rolling_upgrades_happen(ops_manager: MongoDBOpsManager):
    sts = ops_manager.read_statefulset()
    old_generation = sts.metadata.generation
    old_hash = sts.spec.template.metadata.annotations["connectionStringHash"]

    backup_sts = ops_manager.read_backup_statefulset()
    old_backup_generation = backup_sts.metadata.generation
    old_backup_hash = backup_sts.spec.template.metadata.annotations[
        "connectionStringHash"
    ]

    assert old_backup_hash == old_hash

    ops_manager.load()
    ops_manager["spec"]["applicationDatabase"]["version"] = "4.2.0"
    ops_manager.update()

    ops_manager.appdb_status().assert_abandons_phase(phase=Phase.Running)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=300)

    sts = ops_manager.read_statefulset()
    assert (
        sts.metadata.generation == old_generation
    ), "no change should have happened to the Ops Manager stateful set"
    assert (
        sts.spec.template.metadata.annotations["connectionStringHash"] == old_hash
    ), "connection string hash should have remained the same"

    backup_sts = ops_manager.read_backup_statefulset()
    assert (
        backup_sts.metadata.generation == old_backup_generation
    ), "no change should have happened to the backup stateful set"
    assert (
        backup_sts.spec.template.metadata.annotations["connectionStringHash"]
        == old_backup_hash
    ), "connection string hash should have remained the same"


@pytest.mark.e2e_om_ops_manager_secure_config
def test_connection_string_secret_was_updated(ops_manager: MongoDBOpsManager):
    secret = KubernetesTester.read_secret(
        ops_manager.namespace, ops_manager.get_appdb_connection_url_secret_name()
    )
    connection_string = secret["connectionString"]
    assert "new-password" in connection_string
