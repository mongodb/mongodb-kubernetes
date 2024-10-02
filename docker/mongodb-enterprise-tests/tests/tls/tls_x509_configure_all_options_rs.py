import pytest
from kubetester import create_secret, read_secret
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs import (
    ISSUER_CA_NAME,
    create_mongodb_tls_certs,
    create_x509_agent_tls_certs,
    create_x509_mongodb_tls_certs,
)
from kubetester.kubetester import (
    AGENT_WARNING,
    MEMBER_AUTH_WARNING,
    SERVER_WARNING,
    KubernetesTester,
)
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB, Phase
from kubetester.omtester import get_rs_cert_names

MDB_RESOURCE = "test-x509-all-options-rs"


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    create_x509_mongodb_tls_certs(ISSUER_CA_NAME, namespace, MDB_RESOURCE, f"{MDB_RESOURCE}-cert")
    secret_name = f"{MDB_RESOURCE}-cert"
    data = read_secret(namespace, secret_name)
    secret_type = "kubernetes.io/tls"
    create_secret(namespace, f"{MDB_RESOURCE}-clusterfile", data, type=secret_type)


@pytest.fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_x509_agent_tls_certs(issuer, namespace, MDB_RESOURCE)


@pytest.fixture(scope="module")
def mdb(namespace: str, server_certs: str, agent_certs: str, issuer_ca_configmap: str) -> MongoDB:
    res = MongoDB.from_yaml(load_fixture("test-x509-all-options-rs.yaml"), namespace=namespace)
    res["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    return res.update()


@pytest.mark.e2e_tls_x509_configure_all_options_rs
class TestReplicaSetEnableAllOptions(KubernetesTester):
    def test_gets_to_running_state(self, mdb: MongoDB):
        mdb.assert_reaches_phase(Phase.Running, timeout=600)

    def test_ops_manager_state_correctly_updated(self):
        ac_tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        ac_tester.assert_internal_cluster_authentication_enabled()
        ac_tester.assert_authentication_enabled()
