import pytest
from kubernetes import client
from kubetester.kubetester import KubernetesTester, skip_if_local
from kubetester.mongotester import ReplicaSetTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.certs import (
    ISSUER_CA_NAME,
    create_mongodb_tls_certs,
    create_agent_tls_certs,
)

MDB_RESOURCE = "test-tls-upgrade"


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME, namespace, MDB_RESOURCE, f"{MDB_RESOURCE}-cert"
    )


@pytest.fixture(scope="module")
def mdb(namespace: str, server_certs: str, issuer_ca_configmap: str) -> MongoDB:
    res = MongoDB.from_yaml(
        load_fixture("test-tls-base-rs-require-ssl-upgrade.yaml"), namespace=namespace
    )
    res["spec"]["security"] = {"tls": {"ca": issuer_ca_configmap}}
    return res.create()


@pytest.mark.e2e_replica_set_tls_require_upgrade
def test_replica_set_running(mdb: MongoDB):
    mdb.assert_reaches_phase(Phase.Running, timeout=400)


@pytest.mark.e2e_replica_set_tls_require_upgrade
def test_mdb_is_reachable_with_no_ssl(mdb: MongoDB):
    mdb.tester(use_ssl=False).assert_connectivity()


@pytest.mark.e2e_replica_set_tls_require_upgrade
def test_enables_TLS_replica_set(
    mdb: MongoDB, server_certs: str, issuer_ca_configmap: str
):
    mdb.load()
    mdb["spec"]["security"] = {"tls": {"enabled": True}, "ca": issuer_ca_configmap}
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=400)


@pytest.mark.e2e_replica_set_tls_require_upgrade
@skip_if_local()
def test_mdb_is_not_reachable_with_no_ssl():
    ReplicaSetTester(MDB_RESOURCE, 3).assert_no_connection()


@pytest.mark.e2e_replica_set_tls_require_upgrade
@skip_if_local()
def test_mdb_is_reachable_with_ssl(mdb: MongoDB, ca_path: str):
    mdb.tester(use_ssl=True, ca_path=ca_path).assert_connectivity()
