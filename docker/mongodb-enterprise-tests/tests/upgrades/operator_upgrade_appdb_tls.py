from pytest import mark, fixture

from kubetester import get_statefulset
from kubetester.kubetester import fixture as yaml_fixture, skip_if_local
from kubetester.mongodb import Phase
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from tests.conftest import create_appdb_certs

APPDB_NAME = "om-appdb-upgrade-tls-db"

CERT_PREFIX = "prefix"


def appdb_name(namespace: str) -> str:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_appdb_upgrade_tls.yaml"), namespace=namespace
    )
    return "{}-db".format(resource["metadata"]["name"])


@fixture(scope="module")
def appdb_certs_secret(namespace: str, issuer: str):
    return create_appdb_certs(namespace, issuer, APPDB_NAME, cert_prefix=CERT_PREFIX)


@fixture(scope="module")
def ops_manager(
    namespace,
    issuer_ca_configmap: str,
    appdb_certs_secret: str,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_appdb_upgrade_tls.yaml"), namespace=namespace
    )
    resource["spec"]["applicationDatabase"]["security"][
        "certsSecretPrefix"
    ] = CERT_PREFIX

    return resource.create()


@mark.e2e_operator_upgrade_appdb_tls
def test_install_latest_official_operator(official_operator: Operator):
    official_operator.assert_is_running()


@mark.e2e_operator_upgrade_appdb_tls
def test_create_om(ops_manager: MongoDBOpsManager):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=600)


@skip_if_local
@mark.e2e_operator_upgrade_appdb_tls
def test_om_is_ok(ops_manager: MongoDBOpsManager):
    ops_manager.get_om_tester().assert_healthiness()


@mark.e2e_operator_upgrade_appdb_tls
def test_upgrade_operator(default_operator: Operator):
    default_operator.assert_is_running()


@mark.e2e_operator_upgrade_appdb_tls
def test_om_ok(ops_manager: MongoDBOpsManager):
    # status phases are updated gradually - we need to check for each of them (otherwise "check(Running) for OM"
    # will return True right away
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=800)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=500)

    ops_manager.get_om_tester().assert_healthiness()


@mark.e2e_operator_upgrade_appdb_tls
def test_using_official_images(
    namespace: str,
):
    """
    This test ensures that after upgrading from 1.x to 1.20 that our operator automatically replaces the old appdb
    image with the official on
    """
    # -> old quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:5.0.14-ent
    # -> new  quay.io/mongodb/mongodb-enterprise-server:5.0.14-ubi8
    sts = get_statefulset(namespace, APPDB_NAME)
    found_official_image = any(
        [
            "quay.io/mongodb/mongodb-enterprise-server" in container.image
            for container in sts.spec.template.spec.containers
        ]
    )
    assert found_official_image
