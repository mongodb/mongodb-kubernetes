import pytest
from pytest import fixture

from kubetester import MongoDB
from kubetester.kubetester import (
    KubernetesTester,
    skip_if_local,
    fixture as yaml_fixture,
)
from kubetester.mongodb import Phase
from kubetester.certs import (
    ISSUER_CA_NAME,
    create_mongodb_tls_certs,
    create_agent_tls_certs,
)

MDB_RESOURCE_NAME = "tls-replica-set"


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME, namespace, MDB_RESOURCE_NAME, f"{MDB_RESOURCE_NAME}-cert"
    )


@pytest.fixture(scope="module")
def tls_replica_set(
    namespace: str, custom_mdb_version: str, issuer_ca_configmap: str, server_certs: str
) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("test-tls-base-rs-require-ssl.yaml"), MDB_RESOURCE_NAME, namespace
    )

    resource["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    resource.set_version(custom_mdb_version)

    yield resource.create()

    resource.delete()


@pytest.mark.e2e_replica_set_tls_require_and_disable
def test_replica_set_creation(tls_replica_set: MongoDB):
    tls_replica_set.assert_reaches_phase(Phase.Running, timeout=300)


@pytest.mark.e2e_replica_set_tls_require_and_disable
@skip_if_local()
def test_replica_set_is_not_reachable_without_tls(tls_replica_set: MongoDB):
    tester = tls_replica_set.tester(use_ssl=False)
    tester.assert_no_connection()


@pytest.mark.e2e_replica_set_tls_require_and_disable
@skip_if_local()
def test_replica_set_is_reachable_with_tls(tls_replica_set: MongoDB, ca_path: str):
    tester = tls_replica_set.tester(use_ssl=True, ca_path=ca_path)
    tester.assert_connectivity()


@pytest.mark.e2e_replica_set_tls_require_and_disable
def test_configure_prefer_ssl(tls_replica_set: MongoDB):
    """
    Change ssl configuration to preferSSL
    """
    tls_replica_set["spec"]["additionalMongodConfig"] = {
        "net": {"ssl": {"mode": "preferSSL"}}
    }

    tls_replica_set.update()
    tls_replica_set.assert_abandons_phase(Phase.Running)
    tls_replica_set.assert_reaches_phase(Phase.Running, timeout=300)


@pytest.mark.e2e_replica_set_tls_require_and_disable
@skip_if_local()
def test_replica_set_is_reachable_without_ssl_prefer_ssl(tls_replica_set: MongoDB):
    tester = tls_replica_set.tester(use_ssl=False)
    tester.assert_connectivity()


@pytest.mark.e2e_replica_set_tls_require_and_disable
@skip_if_local()
def test_replica_set_is_reachable_with_ssl_prefer_ssl(
    tls_replica_set: MongoDB, ca_path: str
):
    tester = tls_replica_set.tester(use_ssl=True, ca_path=ca_path)
    tester.assert_connectivity()


@pytest.mark.e2e_replica_set_tls_require_and_disable
def test_configure_allow_ssl(tls_replica_set: MongoDB):
    """
    Change ssl configuration to allowSSL
    """
    tls_replica_set["spec"]["additionalMongodConfig"] = {
        "net": {"ssl": {"mode": "allowSSL"}}
    }

    tls_replica_set.update()
    tls_replica_set.assert_abandons_phase(Phase.Running)
    tls_replica_set.assert_reaches_phase(Phase.Running, timeout=300)


@pytest.mark.e2e_replica_set_tls_require_and_disable
@skip_if_local()
def test_replica_set_is_reachable_without_tls_allow_ssl(tls_replica_set: MongoDB):
    tester = tls_replica_set.tester(use_ssl=False)
    tester.assert_connectivity()


@pytest.mark.e2e_replica_set_tls_require_and_disable
@skip_if_local()
def test_replica_set_is_reachable_with_tls_allow_ssl(
    tls_replica_set: MongoDB, ca_path: str
):
    tester = tls_replica_set.tester(use_ssl=True, ca_path=ca_path)
    tester.assert_connectivity()


@pytest.mark.e2e_replica_set_tls_require_and_disable
def test_disabled_ssl(tls_replica_set: MongoDB):
    """
    Disable ssl
    """
    tls_replica_set.load()

    tls_replica_set["spec"]["security"] = {"tls": {"enabled": False}}

    tls_replica_set.update()
    tls_replica_set.assert_abandons_phase(Phase.Running)
    tls_replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@pytest.mark.e2e_replica_set_tls_require_and_disable
@skip_if_local()
def test_replica_set_is_reachable_with_tls_disabled(tls_replica_set: MongoDB):
    tester = tls_replica_set.tester(use_ssl=False)
    tester.assert_connectivity()


@pytest.mark.e2e_replica_set_tls_require_and_disable
@skip_if_local()
def test_replica_set_is_not_reachable_over_ssl_with_ssl_disabled(
    tls_replica_set: MongoDB,
):
    tester = tls_replica_set.tester(use_ssl=True)
    tester.assert_no_connection()
