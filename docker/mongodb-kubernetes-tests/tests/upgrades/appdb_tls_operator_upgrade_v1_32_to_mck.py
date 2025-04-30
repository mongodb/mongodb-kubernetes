from kubetester import get_statefulset
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import Phase
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark
from tests.conftest import (
    LEGACY_MULTI_CLUSTER_OPERATOR_NAME,
    LEGACY_OPERATOR_CHART,
    LEGACY_OPERATOR_IMAGE_NAME,
    LEGACY_OPERATOR_NAME,
    create_appdb_certs,
    get_default_operator,
    install_official_operator,
    is_multi_cluster,
)
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment
from tests.upgrades import downscale_operator_deployment

APPDB_NAME = "om-appdb-upgrade-tls-db"

CERT_PREFIX = "prefix"

# TODO CLOUDP-318100: this test should eventually be updated and not pinned to 1.32 anymore
# TODO: this test runs in the cloudqa variant but still creates OM. We might want to move that to OM variant instead


def appdb_name(namespace: str) -> str:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_appdb_upgrade_tls.yaml"), namespace=namespace
    )
    return "{}-db".format(resource["metadata"]["name"])


@fixture(scope="module")
def appdb_certs_secret(namespace: str, issuer: str):
    return create_appdb_certs(namespace, issuer, APPDB_NAME, cert_prefix=CERT_PREFIX)


# This deployment uses TLS for appdb, which means that monitoring won't work due to missing hostnames in the TLS certs
# This is to test that the operator upgrade will fix monitoring
@fixture(scope="module")
def ops_manager_tls(
    namespace, issuer_ca_configmap: str, appdb_certs_secret: str, custom_version: str, custom_appdb_version: str
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_appdb_upgrade_tls.yaml"), namespace=namespace
    )
    resource["spec"]["applicationDatabase"]["security"]["certsSecretPrefix"] = CERT_PREFIX
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource, om_cluster_spec_list=[1, 0, 0])

    return resource


# This deployment does not use TLS for appdb, therefore monitoring will work
# This is to test that the operator migrates monitoring seamlessly
@fixture(scope="module")
def ops_manager_non_tls(
    namespace, issuer_ca_configmap: str, custom_version: str, custom_appdb_version: str
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_appdb_upgrade_tls.yaml"), namespace=namespace, name="om-appdb-upgrade"
    )
    resource["spec"]["applicationDatabase"]["security"] = None
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource, om_cluster_spec_list=[1, 0, 0])

    return resource


@mark.e2e_appdb_tls_operator_upgrade_v1_32_to_mck
def test_install_latest_official_operator(
    namespace: str,
    managed_security_context,
    operator_installation_config,
    central_cluster_name,
    central_cluster_client,
    member_cluster_clients,
    member_cluster_names,
):
    operator = install_official_operator(
        namespace,
        managed_security_context,
        operator_installation_config,
        central_cluster_name,
        central_cluster_client,
        member_cluster_clients,
        member_cluster_names,
        custom_operator_version="1.32.0",  # latest operator version before fixing the appdb hostnames
        helm_chart_path=LEGACY_OPERATOR_CHART,  # We are testing the upgrade from version fixing appdb hostnames, introduced in MEKO
        operator_name=LEGACY_OPERATOR_NAME,
        operator_image=LEGACY_OPERATOR_IMAGE_NAME,
    )
    operator.assert_is_running()


@mark.e2e_appdb_tls_operator_upgrade_v1_32_to_mck
def test_create_om_tls(ops_manager_tls: MongoDBOpsManager):
    ops_manager_tls.update()
    ops_manager_tls.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager_tls.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager_tls.get_om_tester().assert_healthiness()


@mark.e2e_appdb_tls_operator_upgrade_v1_32_to_mck
def test_create_om_non_tls(ops_manager_non_tls: MongoDBOpsManager):
    ops_manager_non_tls.update()
    ops_manager_non_tls.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager_non_tls.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager_non_tls.get_om_tester().assert_healthiness()
    ops_manager_non_tls.assert_monitoring_data_exists()


@mark.e2e_appdb_tls_operator_upgrade_v1_32_to_mck
def test_downscale_latest_official_operator(namespace: str):
    # Scale down the existing operator deployment to 0. This is needed as long as the
    # `official_operator` fixture installs the MEKO operator.
    deployment_name = LEGACY_MULTI_CLUSTER_OPERATOR_NAME if is_multi_cluster() else LEGACY_OPERATOR_NAME
    downscale_operator_deployment(deployment_name=deployment_name, namespace=namespace)


@mark.e2e_appdb_tls_operator_upgrade_v1_32_to_mck
def test_upgrade_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(
        namespace, operator_installation_config=operator_installation_config, apply_crds_first=True
    )
    operator.assert_is_running()


@mark.e2e_appdb_tls_operator_upgrade_v1_32_to_mck
def test_om_tls_ok(ops_manager_tls: MongoDBOpsManager):
    ops_manager_tls.load()
    ops_manager_tls.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager_tls.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager_tls.get_om_tester().assert_healthiness()


@mark.e2e_appdb_tls_operator_upgrade_v1_32_to_mck
def test_om_non_tls_ok(ops_manager_non_tls: MongoDBOpsManager):
    ops_manager_non_tls.load()
    ops_manager_non_tls.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager_non_tls.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager_non_tls.get_om_tester().assert_healthiness()


@mark.e2e_appdb_tls_operator_upgrade_v1_32_to_mck
def test_appdb_tls_hostnames(ops_manager_tls: MongoDBOpsManager):
    ops_manager_tls.load()
    ops_manager_tls.assert_appdb_preferred_hostnames_are_added()
    ops_manager_tls.assert_appdb_hostnames_are_correct()
    ops_manager_tls.assert_appdb_monitoring_group_was_created()
    ops_manager_tls.assert_monitoring_data_exists()


@mark.e2e_appdb_tls_operator_upgrade_v1_32_to_mck
def test_appdb_non_tls_hostnames(ops_manager_non_tls: MongoDBOpsManager):
    ops_manager_non_tls.load()
    ops_manager_non_tls.assert_appdb_preferred_hostnames_are_added()
    ops_manager_non_tls.assert_appdb_hostnames_are_correct()
    ops_manager_non_tls.assert_appdb_monitoring_group_was_created()
    ops_manager_non_tls.assert_monitoring_data_exists()


@mark.e2e_appdb_tls_operator_upgrade_v1_32_to_mck
def test_using_official_images(namespace: str, ops_manager_tls: MongoDBOpsManager):
    """
    This test ensures that after upgrading from 1.x to 1.20 that our operator automatically replaces the old appdb
    image with the official on
    """
    # -> old quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:5.0.14-ent
    # -> new  quay.io/mongodb/mongodb-enterprise-server:5.0.14-ubi8
    ops_manager_tls.load()
    sts = ops_manager_tls.read_appdb_statefulset()
    found_official_image = any(
        [
            "quay.io/mongodb/mongodb-enterprise-server" in container.image
            for container in sts.spec.template.spec.containers
        ]
    )
    assert found_official_image
