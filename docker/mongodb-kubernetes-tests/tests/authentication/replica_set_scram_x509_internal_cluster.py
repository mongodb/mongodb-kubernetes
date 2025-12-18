from kubetester import create_or_update_secret, read_secret
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs import ISSUER_CA_NAME, create_x509_agent_tls_certs, create_x509_mongodb_tls_certs
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.phase import Phase
from pytest import fixture, mark

MDB_RESOURCE = "my-replica-set"


@fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    create_x509_mongodb_tls_certs(ISSUER_CA_NAME, namespace, MDB_RESOURCE, f"{MDB_RESOURCE}-cert")
    secret_name = f"{MDB_RESOURCE}-cert"
    data = read_secret(namespace, secret_name)
    secret_type = "kubernetes.io/tls"
    create_or_update_secret(namespace, f"{MDB_RESOURCE}-clusterfile", data, type=secret_type)


@fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_x509_agent_tls_certs(issuer, namespace, MDB_RESOURCE)


@fixture(scope="module")
def mdb(namespace: str, server_certs: str, agent_certs: str, issuer_ca_configmap: str) -> MongoDB:
    res = MongoDB.from_yaml(
        load_fixture("replica-set-scram-sha-256-x509-internal-cluster.yaml"),
        namespace=namespace,
    )
    res["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    return res.update()


@mark.e2e_replica_set_scram_x509_internal_cluster
class TestReplicaSetScramX509Internal(KubernetesTester):
    def test_mdb_is_running(self, mdb: MongoDB):
        mdb.assert_reaches_phase(Phase.Running, timeout=600)

    def test_ops_manager_state_was_updated_correctly(self):
        ac_tester = AutomationConfigTester(self.get_automation_config())
        ac_tester.assert_authentication_enabled()
        ac_tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
        ac_tester.assert_expected_users(0)
        ac_tester.assert_authoritative_set(True)
        ac_tester.assert_internal_cluster_authentication_enabled()
