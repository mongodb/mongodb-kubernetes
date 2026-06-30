import pytest
from kubetester import try_load
from kubetester.certs import ISSUER_CA_NAME, create_agent_tls_certs, create_mongodb_tls_certs
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongotester import ReplicaSetTester
from kubetester.operator import Operator
from kubetester.phase import Phase

MDB_RESOURCE = "my-replica-set"
CERT_SECRET_PREFIX = "prefix"


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME, namespace, MDB_RESOURCE, f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert"
    )


@pytest.fixture(scope="module")
def mdb(namespace: str, server_certs: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("replica-set.yaml"), namespace=namespace)
    try_load(resource)
    return resource


@pytest.fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_agent_tls_certs(issuer, namespace, MDB_RESOURCE, secret_prefix=CERT_SECRET_PREFIX)


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_rs
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_rs
def test_mdb_running(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=400)


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_rs
def test_connectivity():
    tester = ReplicaSetTester(MDB_RESOURCE, 3)
    tester.assert_connectivity()


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_rs
def test_enable_tls(mdb: MongoDB, issuer_ca_configmap: str):
    mdb["spec"]["security"] = {
        "certsSecretPrefix": CERT_SECRET_PREFIX,
        "tls": {"ca": issuer_ca_configmap},
    }
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=400)


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_rs
def test_connectivity_with_ssl(mdb: MongoDB, ca_path: str):
    tester = mdb.tester(use_ssl=True, ca_path=ca_path)
    tester.assert_connectivity()


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_rs
def test_enable_x509(mdb: MongoDB, agent_certs: str):
    mdb["spec"]["security"]["authentication"] = {
        "agents": {"mode": "X509"},
        "enabled": True,
        "modes": ["X509"],
    }
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=1200)
