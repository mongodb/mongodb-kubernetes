import pytest
from kubetester.certs import (
    ISSUER_CA_NAME,
    create_agent_tls_certs,
    create_mongodb_tls_certs,
)
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongotester import StandaloneTester
from kubetester.operator import Operator

MDB_RESOURCE = "my-standalone"


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    return create_mongodb_tls_certs(ISSUER_CA_NAME, namespace, MDB_RESOURCE, f"{MDB_RESOURCE}-cert", replicas=1)


@pytest.fixture(scope="module")
def mdb(namespace: str, server_certs: str, issuer_ca_configmap: str) -> MongoDB:
    res = MongoDB.from_yaml(load_fixture("standalone.yaml"), namespace=namespace)
    res["spec"]["security"] = {"tls": {"ca": issuer_ca_configmap}}
    return res.create()


@pytest.fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_agent_tls_certs(issuer, namespace, MDB_RESOURCE)


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_st
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_st
def test_mdb_running(mdb: MongoDB):
    mdb.assert_reaches_phase(Phase.Running, timeout=400)


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_st
def test_connectivity():
    tester = StandaloneTester(MDB_RESOURCE)
    tester.assert_connectivity()


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_st
def test_enable_x509(mdb: MongoDB, agent_certs: str):
    mdb.load()
    mdb["spec"]["security"] = {
        "authentication": {"enabled": True},
        "modes": ["X509"],
    }

    mdb.assert_reaches_phase(Phase.Running, timeout=400)
