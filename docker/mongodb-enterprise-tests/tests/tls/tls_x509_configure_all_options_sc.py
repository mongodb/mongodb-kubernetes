import pytest

from pytest import mark, fixture
from kubetester.kubetester import KubernetesTester
from kubetester.omtester import get_sc_cert_names
from kubetester import create_secret, read_secret
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester import find_fixture
from kubetester.kubetester import fixture as load_fixture
from kubetester.certs import (
    ISSUER_CA_NAME,
    create_x509_mongodb_tls_certs,
    create_x509_agent_tls_certs,
    create_sharded_cluster_certs,
)

MDB_RESOURCE_NAME = "test-x509-all-options-sc"
from kubetester.mongodb import MongoDB, Phase


@fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_x509_agent_tls_certs(issuer, namespace, MDB_RESOURCE_NAME)


@fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    create_sharded_cluster_certs(
        namespace,
        MDB_RESOURCE_NAME,
        shards=1,
        mongos_per_shard=3,
        config_servers=3,
        mongos=2,
        internal_auth=True,
        x509_certs=True,
    )


@fixture(scope="module")
def sharded_cluster(
    namespace: str, server_certs: str, agent_certs: str, issuer_ca_configmap: str
) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("test-x509-all-options-sc.yaml"),
        namespace=namespace,
    )
    resource["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    yield resource.create()


@pytest.mark.e2e_tls_x509_configure_all_options_sc
class TestShardedClusterEnableAllOptions(KubernetesTester):
    def test_gets_to_running_state(self, sharded_cluster: MongoDB):
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=600)

    def test_ops_manager_state_correctly_updated(self):
        ac_tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        ac_tester.assert_internal_cluster_authentication_enabled()
        ac_tester.assert_authentication_enabled()
        ac_tester.assert_expected_users(0)

    # TODO: use /mongodb-automation/server.pem but doesn't exist on test pod
    # def test_mdb_is_reachable_with_no_ssl(self):
    #     mongo_tester = ShardedClusterTester(mdb_resource, 2, ssl=True)
    #     mongo_tester.assert_connectivity()
