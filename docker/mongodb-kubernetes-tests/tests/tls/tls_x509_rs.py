import pytest
from kubetester.certs import (
    ISSUER_CA_NAME,
    create_agent_tls_certs,
    create_mongodb_tls_certs,
)
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongotester import ReplicaSetTester
from kubetester.operator import Operator

MDB_RESOURCE = "test-x509-rs"


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    return create_mongodb_tls_certs(ISSUER_CA_NAME, namespace, MDB_RESOURCE, f"{MDB_RESOURCE}-cert")


@pytest.fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_agent_tls_certs(issuer, namespace, MDB_RESOURCE)


@pytest.fixture(scope="module")
def mdb(namespace: str, server_certs: str, agent_certs: str, issuer_ca_configmap: str) -> MongoDB:
    res = MongoDB.from_yaml(load_fixture("test-x509-rs.yaml"), namespace=namespace)
    res["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    return res.create()


@pytest.mark.e2e_tls_x509_rs
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@pytest.mark.e2e_tls_x509_rs
class TestReplicaSetWithNoTLSCreation(KubernetesTester):
    def test_gets_to_running_state(self, mdb: MongoDB):
        mdb.assert_reaches_phase(Phase.Running, timeout=1200)

    @skip_if_local
    def test_mdb_is_reachable_with_no_ssl(self):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_no_connection()

    @skip_if_local
    def test_mdb_is_reachable_with_ssl(self):
        # This one will also fails, as it expects x509 client certs which we are not passing.
        tester = ReplicaSetTester(MDB_RESOURCE, 3, ssl=True)
        tester.assert_no_connection()


@pytest.mark.e2e_tls_x509_rs
class TestReplicaSetWithNoTLSDeletion(KubernetesTester):
    """
    delete:
      file: test-x509-rs.yaml
      wait_until: mongo_resource_deleted_no_om
      timeout: 240
    """

    def test_deletion(self):
        assert True
