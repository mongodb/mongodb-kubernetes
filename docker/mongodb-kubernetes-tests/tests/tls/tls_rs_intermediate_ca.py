import pytest
from kubetester.certs import create_mongodb_tls_certs
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase

MDB_RESOURCE_NAME = "test-tls-rs-intermediate-ca"


@pytest.fixture(scope="module")
def server_certs(intermediate_issuer: str, namespace: str):
    return create_mongodb_tls_certs(
        intermediate_issuer,
        namespace,
        MDB_RESOURCE_NAME,
        f"{MDB_RESOURCE_NAME}-cert",
        additional_domains=[
            "test-tls-rs-intermediate-ca-0.additional-cert-test.com",
            "test-tls-rs-intermediate-ca-1.additional-cert-test.com",
            "test-tls-rs-intermediate-ca-2.additional-cert-test.com",
        ],
    )


@pytest.fixture(scope="module")
def mdb(namespace: str, server_certs: str, issuer_ca_configmap: str) -> MongoDB:
    res = MongoDB.from_yaml(
        load_fixture("test-tls-additional-domains.yaml"),
        namespace=namespace,
        name=MDB_RESOURCE_NAME,
    )
    res["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    return res.create()


@pytest.mark.e2e_tls_rs_intermediate_ca
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@pytest.mark.e2e_tls_rs_intermediate_ca
class TestReplicaSetWithAdditionalCertDomains(KubernetesTester):
    def test_replica_set_is_running(self, mdb: MongoDB):
        mdb.assert_reaches_phase(Phase.Running, timeout=400)

    @skip_if_local
    def test_can_still_connect(self, mdb: MongoDB, ca_path: str):
        tester = mdb.tester(use_ssl=True, ca_path=ca_path)
        tester.assert_connectivity()
