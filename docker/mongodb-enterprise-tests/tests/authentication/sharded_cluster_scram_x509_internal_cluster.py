from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs import (
    ISSUER_CA_NAME,
    create_sharded_cluster_certs,
    create_x509_agent_tls_certs,
    create_x509_mongodb_tls_certs,
)
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as find_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.omtester import get_sc_cert_names
from pytest import fixture, mark

MDB_RESOURCE_NAME = "sharded-cluster-scram-sha-256"


@fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_x509_agent_tls_certs(issuer, namespace, MDB_RESOURCE_NAME)


@fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    create_sharded_cluster_certs(
        namespace,
        MDB_RESOURCE_NAME,
        shards=1,
        mongod_per_shard=3,
        config_servers=3,
        mongos=2,
        internal_auth=True,
        x509_certs=True,
    )


@fixture(scope="module")
def sharded_cluster(namespace: str, server_certs, agent_certs: str, issuer_ca_configmap: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("sharded-cluster-scram-sha-256-x509-internal-cluster.yaml"),
        namespace=namespace,
    )
    resource["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    yield resource.create()


@mark.e2e_sharded_cluster_scram_x509_internal_cluster
class TestReplicaSetScramX509Internal(KubernetesTester):
    def test_create_resource(self, sharded_cluster: MongoDB):
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1200)

    def test_ops_manager_state_was_updated_correctly(self):
        ac_tester = AutomationConfigTester(self.get_automation_config())
        ac_tester.assert_authentication_enabled()
        ac_tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
        ac_tester.assert_internal_cluster_authentication_enabled()

        ac_tester.assert_expected_users(0)
        ac_tester.assert_authoritative_set(True)
