import pytest
from pytest import fixture

from kubetester import MongoDB
from kubetester.kubetester import (
    KubernetesTester,
    skip_if_local,
    fixture as yaml_fixture,
)
from kubetester.mongodb import Phase

MDB_RESOURCE_NAME = "tls-replica-set"


def csr_names(namespace):
    return ["{}-{}.{}".format(MDB_RESOURCE_NAME, i, namespace) for i in range(3)]


@fixture(scope="module")
def tls_replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("test-tls-base-rs-require-ssl.yaml"), MDB_RESOURCE_NAME, namespace
    )
    resource.set_version(custom_mdb_version)

    yield resource.create()

    resource.delete()


@pytest.mark.e2e_replica_set_tls_require_and_disable
def test_replica_set_creation(tls_replica_set: MongoDB):
    """
    Creates a MongoDB object with the ssl attribute on. The MongoDB object will go to Pending
    state because of missing certificates.
    """
    tls_replica_set.assert_reaches_phase(
        Phase.Pending,
        timeout=240,
        msg_regexp=f"Not all certificates have been approved by Kubernetes CA for {MDB_RESOURCE_NAME}",
    )


@pytest.mark.e2e_replica_set_tls_require_and_disable
def test_replica_set_gets_into_running_state(namespace: str, tls_replica_set: MongoDB):
    """
    Ensure the resource reaches Running state after certificate approval
    """
    for cert in KubernetesTester.yield_existing_csrs(csr_names(namespace)):
        KubernetesTester.approve_certificate(cert)
    tls_replica_set.assert_reaches_phase(Phase.Running, timeout=300)


@pytest.mark.e2e_replica_set_tls_require_and_disable
@skip_if_local()
def test_replica_set_is_not_reachable_without_tls(tls_replica_set: MongoDB):
    tester = tls_replica_set.tester(use_ssl=False)
    tester.assert_no_connection()


@pytest.mark.e2e_replica_set_tls_require_and_disable
@skip_if_local()
def test_replica_set_is_reachable_with_tls(tls_replica_set: MongoDB):
    tester = tls_replica_set.tester(use_ssl=True)
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
def test_replica_set_is_reachable_with_ssl_prefer_ssl(tls_replica_set: MongoDB):
    tester = tls_replica_set.tester(use_ssl=True)
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
def test_replica_set_is_reachable_with_tls_allow_ssl(tls_replica_set: MongoDB):
    tester = tls_replica_set.tester(use_ssl=True)
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
