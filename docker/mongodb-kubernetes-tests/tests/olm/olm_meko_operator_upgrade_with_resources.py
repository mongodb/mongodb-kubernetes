import kubernetes
import pytest
from kubeobject import CustomObject
from kubetester import create_or_update_secret, get_default_storage_class, try_load
from kubetester.awss3client import AwsS3Client
from kubetester.certs import create_sharded_cluster_certs
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import get_default_architecture, run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests import test_logger
from tests.conftest import LEGACY_OPERATOR_NAME, OPERATOR_NAME
from tests.olm.olm_test_commons import (
    get_catalog_image,
    get_catalog_source_resource,
    get_current_operator_version,
    get_latest_released_operator_version,
    get_operator_group_resource,
    get_subscription_custom_object,
    increment_patch_version,
    wait_for_operator_ready,
)
from tests.opsmanager.om_ops_manager_backup import create_aws_secret, create_s3_bucket

logger = test_logger.get_test_logger(__name__)

# See docs how to run this locally: https://wiki.corp.mongodb.com/display/MMS/E2E+Tests+Notes#E2ETestsNotes-OLMtests

# This test performs operator migration from the latest MEKO to MCK while having OM and MongoDB resources deployed.
# It uses the uninstall-then-install approach since operatorhub.io and other catalogs
# do not allow cross-package upgrades with the "replaces" directive.
# The test performs the following actions:
#  - deploy latest released MEKO operator using OLM
#  - deploy OM
#  - deploy backup-required MongoDB: oplog, s3, blockstore
#  - deploy TLS-enabled sharded MongoDB
#  - check everything is running
#  - uninstall MEKO operator by deleting Subscription and ClusterServiceVersion resources
#  - install MCK operator using OLM with a fresh subscription
#  - wait for resources to be rolling-updated due to updated stateful sets by the new operator
#  - check everything is running and connectable


@fixture
def catalog_source(namespace: str, version_id: str):
    current_operator_version = get_current_operator_version()
    incremented_operator_version = increment_patch_version(current_operator_version)

    get_operator_group_resource(namespace, namespace).update()
    catalog_source_resource = get_catalog_source_resource(
        namespace, get_catalog_image(f"{incremented_operator_version}-{version_id}")
    )
    catalog_source_resource.update()

    return catalog_source_resource


@fixture
def meko_subscription(namespace: str, catalog_source: CustomObject):
    """
    Create subscription for the MEKO operator.
    """
    static_value = get_default_architecture()
    return get_subscription_custom_object(
        LEGACY_OPERATOR_NAME,
        namespace,
        {
            "channel": "stable",  # stable channel contains latest released operator in RedHat's certified repository
            "name": "mongodb-enterprise",
            "source": catalog_source.name,
            "sourceNamespace": namespace,
            "installPlanApproval": "Automatic",
            # In certified OpenShift bundles we have this enabled, so the operator is not defining security context (it's managed globally by OpenShift).
            # In Kind this will result in empty security contexts and problems deployments with filesystem permissions.
            "config": {
                "env": [
                    {"name": "MANAGED_SECURITY_CONTEXT", "value": "false"},
                    {"name": "OPERATOR_ENV", "value": "dev"},
                    {"name": "MDB_DEFAULT_ARCHITECTURE", "value": static_value},
                    {"name": "MDB_OPERATOR_TELEMETRY_SEND_ENABLED", "value": "false"},
                ]
            },
        },
    )


def get_mck_subscription_object(namespace: str, catalog_source: CustomObject):
    """
    Create a subscription object for the MCK operator.
    This is a separate function (not a fixture) so it can be called after uninstalling MEKO.
    """
    static_value = get_default_architecture()
    return get_subscription_custom_object(
        OPERATOR_NAME,
        namespace,
        {
            "channel": "migration",
            "name": "mongodb-kubernetes",
            "source": catalog_source.name,
            "sourceNamespace": namespace,
            "installPlanApproval": "Automatic",
            "config": {
                "env": [
                    {"name": "MANAGED_SECURITY_CONTEXT", "value": "false"},
                    {"name": "OPERATOR_ENV", "value": "dev"},
                    {"name": "MDB_DEFAULT_ARCHITECTURE", "value": static_value},
                    {"name": "MDB_OPERATOR_TELEMETRY_SEND_ENABLED", "value": "false"},
                ]
            },
        },
    )


@fixture
def latest_released_meko_version():
    return get_latest_released_operator_version("mongodb-enterprise")


@pytest.mark.e2e_olm_meko_operator_upgrade_with_resources
def test_meko_install_stable_operator_version(
    namespace: str,
    latest_released_meko_version: str,
    catalog_source: CustomObject,
    meko_subscription: CustomObject,
):
    meko_subscription.update()
    wait_for_operator_ready(namespace, LEGACY_OPERATOR_NAME, f"mongodb-enterprise.v{latest_released_meko_version}")


# install resources on the latest released version of the operator


@fixture(scope="module")
def ops_manager(namespace: str) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("olm_ops_manager_backup.yaml"), namespace=namespace
    )

    try_load(resource)
    return resource


@fixture(scope="module")
def s3_bucket(aws_s3_client: AwsS3Client, namespace: str) -> str:
    create_aws_secret(aws_s3_client, "my-s3-secret", namespace)
    yield from create_s3_bucket(aws_s3_client, "test-bucket-s3")


@pytest.mark.e2e_olm_meko_operator_upgrade_with_resources
def test_create_om(
    ops_manager: MongoDBOpsManager,
    s3_bucket: str,
    custom_version: str,
    custom_appdb_version: str,
):
    try_load(ops_manager)
    ops_manager["spec"]["backup"]["s3Stores"][0]["s3BucketName"] = s3_bucket
    ops_manager["spec"]["backup"]["headDB"]["storageClass"] = get_default_storage_class()
    ops_manager["spec"]["backup"]["members"] = 2

    ops_manager.set_version(custom_version)
    ops_manager.set_appdb_version(custom_appdb_version)
    ops_manager.allow_mdb_rc_versions()

    ops_manager.update()


def wait_for_om_healthy_response(ops_manager: MongoDBOpsManager):
    def wait_for_om_healthy_response_fn():
        status_code, status_response = ops_manager.get_om_tester().check_healthiness()
        if status_code == 200:
            return True, f"Got healthy status_code=200: {status_response}"
        else:
            return False, f"Got unhealthy status_code={status_code}: {status_response}"

    run_periodically(
        wait_for_om_healthy_response_fn,
        timeout=300,
        msg=f"OM returning healthy response",
    )


@pytest.mark.e2e_olm_meko_operator_upgrade_with_resources
def test_om_connectivity(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
    ops_manager.backup_status().assert_reaches_phase(Phase.Pending)

    wait_for_om_healthy_response(ops_manager)


# sharded mongodb fixtures


@fixture(scope="module")
def mdb_sharded_certs(issuer: str, namespace: str):
    create_sharded_cluster_certs(
        namespace,
        "mdb-sharded",
        shards=1,
        mongod_per_shard=2,
        config_servers=1,
        mongos=1,
        secret_prefix="prefix-",
    )


@fixture
def mdb_sharded(
    ops_manager: MongoDBOpsManager,
    namespace,
    custom_mdb_version: str,
    issuer_ca_configmap: str,
    mdb_sharded_certs,
):
    resource = MongoDB.from_yaml(
        yaml_fixture("olm_sharded_cluster_for_om.yaml"),
        namespace=namespace,
        name="mdb-sharded",
    ).configure(ops_manager, "mdb-sharded")
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["security"] = {
        "tls": {
            "ca": issuer_ca_configmap,
        },
    }
    resource.configure_backup(mode="disabled")
    resource.update()
    return resource


# OpsManager backup-backing databases


@fixture(scope="module")
def oplog_replica_set(ops_manager, namespace, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("olm_replica_set_for_om.yaml"),
        namespace=namespace,
        name="my-mongodb-oplog",
    ).configure(ops_manager, "oplog")
    resource.set_version(custom_mdb_version)
    return resource.update()


@fixture(scope="module")
def s3_replica_set(ops_manager, namespace, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("olm_replica_set_for_om.yaml"),
        namespace=namespace,
        name="my-mongodb-s3",
    ).configure(ops_manager, "s3metadata")
    resource.set_version(custom_mdb_version)
    return resource.update()


@fixture(scope="module")
def blockstore_replica_set(ops_manager, namespace, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("olm_replica_set_for_om.yaml"),
        namespace=namespace,
        name="my-mongodb-blockstore",
    ).configure(ops_manager, "blockstore")
    resource.set_version(custom_mdb_version)
    return resource.update()


@fixture(scope="module")
def blockstore_user(namespace, blockstore_replica_set: MongoDB) -> MongoDBUser:
    return create_secret_and_user(
        namespace,
        "blockstore-user",
        blockstore_replica_set.name,
        "blockstore-user-password-secret",
        "Passw0rd.",
    )


@fixture(scope="module")
def oplog_user(namespace, oplog_replica_set: MongoDB) -> MongoDBUser:
    return create_secret_and_user(
        namespace,
        "oplog-user",
        oplog_replica_set.name,
        "oplog-user-password-secret",
        "Passw0rd.",
    )


def create_secret_and_user(
    namespace: str, name: str, replica_set_name: str, secret_name: str, password: str
) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(
        yaml_fixture("olm_scram_sha_user_backing_db.yaml"),
        namespace=namespace,
        name=name,
    )
    resource["spec"]["mongodbResourceRef"]["name"] = replica_set_name
    resource["spec"]["passwordSecretKeyRef"]["name"] = secret_name
    create_or_update_secret(namespace, secret_name, {"password": password})
    return resource.update()


@pytest.mark.e2e_olm_meko_operator_upgrade_with_resources
def test_resources_created(
    oplog_replica_set: MongoDB,
    s3_replica_set: MongoDB,
    blockstore_replica_set: MongoDB,
    mdb_sharded: MongoDB,
    blockstore_user: MongoDBUser,
    oplog_user: MongoDBUser,
):
    """Creates mongodb databases all at once"""
    oplog_replica_set.assert_reaches_phase(Phase.Running)
    s3_replica_set.assert_reaches_phase(Phase.Running)
    blockstore_replica_set.assert_reaches_phase(Phase.Running)
    mdb_sharded.assert_reaches_phase(Phase.Running)


@pytest.mark.e2e_olm_meko_operator_upgrade_with_resources
def test_set_backup_users(
    ops_manager: MongoDBOpsManager,
    oplog_user: MongoDBUser,
    blockstore_user: MongoDBUser,
):
    ops_manager.load()
    ops_manager["spec"]["backup"]["opLogStores"][0]["mongodbUserRef"] = {"name": oplog_user.name}
    ops_manager["spec"]["backup"]["blockStores"][0]["mongodbUserRef"] = {"name": blockstore_user.name}
    ops_manager.update()

    ops_manager.backup_status().assert_reaches_phase(Phase.Running, ignore_errors=True)

    assert ops_manager.backup_status().get_message() is None


@pytest.mark.e2e_olm_meko_operator_upgrade_with_resources
def test_om_connectivity_with_backup(
    ops_manager: MongoDBOpsManager,
    oplog_replica_set: MongoDB,
    s3_replica_set: MongoDB,
):
    wait_for_om_healthy_response(ops_manager)

    ops_manager.om_status().assert_reaches_phase(Phase.Running)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
    ops_manager.backup_status().assert_reaches_phase(Phase.Running)


@pytest.mark.e2e_olm_meko_operator_upgrade_with_resources
def test_resources_in_running_state_before_upgrade(
    ops_manager: MongoDBOpsManager,
    oplog_replica_set: MongoDB,
    blockstore_replica_set: MongoDB,
    s3_replica_set: MongoDB,
    mdb_sharded: MongoDB,
):
    ops_manager.om_status().assert_reaches_phase(Phase.Running)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
    ops_manager.backup_status().assert_reaches_phase(Phase.Running)
    oplog_replica_set.assert_reaches_phase(Phase.Running)
    blockstore_replica_set.assert_reaches_phase(Phase.Running)
    s3_replica_set.assert_reaches_phase(Phase.Running)
    mdb_sharded.assert_reaches_phase(Phase.Running)


# uninstall MEKO and install MCK operator instead


def uninstall_meko_operator(namespace: str, meko_subscription: CustomObject):
    """Uninstall the MEKO operator by deleting Subscription and ClusterServiceVersion"""

    # Load the subscription from API server
    # so we can get CSV name from status
    meko_subscription.load()
    csv_name = meko_subscription["status"]["installedCSV"]

    # Delete the subscription
    meko_subscription.delete()

    # Delete ClusterServiceVersion
    api_instance = kubernetes.client.CustomObjectsApi()
    api_instance.delete_namespaced_custom_object(
        group="operators.coreos.com",
        version="v1alpha1",
        namespace=namespace,
        plural="clusterserviceversions",
        name=csv_name,
    )


@pytest.mark.e2e_olm_meko_operator_upgrade_with_resources
def test_uninstall_meko_operator(
    namespace: str,
    meko_subscription: CustomObject,
):
    # Uninstall the MEKO operator
    uninstall_meko_operator(namespace, meko_subscription)

    # Get a list of all statefulsets
    api_instance = kubernetes.client.AppsV1Api()
    statefulsets = api_instance.list_namespaced_stateful_set(namespace)

    # Kill one pod from each statefulset to simulate reschedule
    for sts in statefulsets.items:
        sts_name = sts.metadata.name
        logger.info(f"Processing StatefulSet {sts_name}")

        # Get pods for this statefulset
        if sts.spec.selector and sts.spec.selector.match_labels:
            # Build label selector string from match_labels dictionary
            selector_parts = []
            for key, value in sts.spec.selector.match_labels.items():
                selector_parts.append(f"{key}={value}")
            label_selector = ",".join(selector_parts)

            pods = kubernetes.client.CoreV1Api().list_namespaced_pod(namespace, label_selector=label_selector)

            if pods.items:
                # Delete the first pod
                pod_name = pods.items[0].metadata.name
                logger.info(f"Deleting pod {pod_name} from StatefulSet {sts_name}")
                kubernetes.client.CoreV1Api().delete_namespaced_pod(
                    name=pod_name, namespace=namespace, body=kubernetes.client.V1DeleteOptions()
                )

    # Wait for all statefulsets to be ready again
    def all_statefulsets_ready():
        for sts in api_instance.list_namespaced_stateful_set(namespace).items:
            if sts.status.ready_replicas != sts.status.replicas:
                return (
                    False,
                    f"StatefulSet {sts.metadata.name} has {sts.status.ready_replicas}/{sts.status.replicas} ready replicas",
                )
        return True, "All StatefulSets are ready"

    run_periodically(
        all_statefulsets_ready, timeout=600, msg=f"Waiting for all StatefulSets to be ready after pod deletion"
    )


@pytest.mark.e2e_olm_meko_operator_upgrade_with_resources
def test_connectivity_after_meko_uninstall(
    ca_path: str,
    ops_manager: MongoDBOpsManager,
    oplog_replica_set: MongoDB,
    blockstore_replica_set: MongoDB,
    s3_replica_set: MongoDB,
    mdb_sharded: MongoDB,
):
    """Verify resources are still connectable after MEKO operator uninstall but before MCK operator install"""
    wait_for_om_healthy_response(ops_manager)

    oplog_replica_set.assert_connectivity()
    blockstore_replica_set.assert_connectivity()
    s3_replica_set.assert_connectivity()
    mdb_sharded.assert_connectivity(ca_path=ca_path)


@pytest.mark.e2e_olm_meko_operator_upgrade_with_resources
def test_install_mck_operator(
    namespace: str,
    catalog_source: CustomObject,
):
    current_operator_version = get_current_operator_version()
    incremented_operator_version = increment_patch_version(current_operator_version)

    # Create MCK subscription
    mck_subscription = get_mck_subscription_object(namespace, catalog_source)
    mck_subscription.update()

    wait_for_operator_ready(namespace, OPERATOR_NAME, f"mongodb-kubernetes.v{incremented_operator_version}")


@pytest.mark.e2e_olm_meko_operator_upgrade_with_resources
def test_one_resource_not_in_running_state(ops_manager: MongoDBOpsManager):
    # Wait for the first resource to become reconciling after operator replacement.
    # This confirms the MCK operator has started reconciling the resources
    ops_manager.om_status().assert_reaches_phase(Phase.Pending, timeout=600)


@pytest.mark.e2e_olm_meko_operator_upgrade_with_resources
def test_resources_in_running_state_after_migration(
    ops_manager: MongoDBOpsManager,
    oplog_replica_set: MongoDB,
    blockstore_replica_set: MongoDB,
    s3_replica_set: MongoDB,
    mdb_sharded: MongoDB,
):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1000)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1000)
    ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=1000)
    # Once the first resource reaches running phase, the subsequent check can have shorter timeouts
    oplog_replica_set.assert_reaches_phase(Phase.Running, timeout=400)
    blockstore_replica_set.assert_reaches_phase(Phase.Running, timeout=200)
    s3_replica_set.assert_reaches_phase(Phase.Running, timeout=200)
    mdb_sharded.assert_reaches_phase(Phase.Running, timeout=200)


@pytest.mark.e2e_olm_meko_operator_upgrade_with_resources
def test_resources_connectivity_after_migration(
    ca_path: str,
    ops_manager: MongoDBOpsManager,
    oplog_replica_set: MongoDB,
    blockstore_replica_set: MongoDB,
    s3_replica_set: MongoDB,
    mdb_sharded: MongoDB,
):
    wait_for_om_healthy_response(ops_manager)

    oplog_replica_set.assert_connectivity()
    blockstore_replica_set.assert_connectivity()
    s3_replica_set.assert_connectivity()
    mdb_sharded.assert_connectivity(ca_path=ca_path)
