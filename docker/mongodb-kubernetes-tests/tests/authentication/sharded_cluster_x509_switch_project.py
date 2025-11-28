import pytest
from kubetester import (
    try_load,
)
from kubetester.certs import (
    ISSUER_CA_NAME,
    create_sharded_cluster_certs,
    create_x509_agent_tls_certs,
)
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB

from .helper_sharded_cluster_switch_project import (
    ShardedClusterSwitchProjectHelper,
)

MDB_RESOURCE_NAME = "sharded-cluster-x509-switch-project"


@pytest.fixture(scope="function")
def sharded_cluster(namespace: str, server_certs: str, agent_certs: str, issuer_ca_configmap: str) -> MongoDB:

    resource = MongoDB.from_yaml(
        load_fixture("sharded-cluster-x509-internal-cluster-auth-transition.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )

    if try_load(resource):
        return resource

    resource["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    return resource.update()


@pytest.fixture(scope="function")
def server_certs(issuer: str, namespace: str):
    create_sharded_cluster_certs(
        namespace,
        MDB_RESOURCE_NAME,
        shards=2,
        mongod_per_shard=3,
        config_servers=1,
        mongos=1,
    )


@pytest.fixture(scope="function")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_x509_agent_tls_certs(issuer, namespace, MDB_RESOURCE_NAME)


@pytest.fixture(scope="function")
def testhelper(sharded_cluster: MongoDB, namespace: str) -> ShardedClusterSwitchProjectHelper:
    return ShardedClusterSwitchProjectHelper(
        sharded_cluster=sharded_cluster,
        namespace=namespace,
        authentication_mechanism="MONGODB-X509",
        expected_num_deployment_auth_mechanisms=1,
    )


@pytest.mark.e2e_sharded_cluster_x509_switch_project
class TestShardedClusterCreationAndProjectSwitch(KubernetesTester):
    """
    E2E test suite for sharded cluster creation, user connectivity with X509 authentication and switching Ops Manager project reference.
    """

    def test_create_sharded_cluster(self, testhelper: ShardedClusterSwitchProjectHelper):
        testhelper.test_create_sharded_cluster()

    def test_ops_manager_state_correctly_updated_in_initial_sharded_cluster(
        self, testhelper: ShardedClusterSwitchProjectHelper
    ):
        testhelper.test_ops_manager_state_with_expected_authentication(expected_users=0)

    def test_switch_sharded_cluster_project(self, testhelper: ShardedClusterSwitchProjectHelper):
        testhelper.test_switch_sharded_cluster_project()

    def test_ops_manager_state_correctly_updated_after_switch(self, testhelper: ShardedClusterSwitchProjectHelper):
        testhelper.test_ops_manager_state_with_expected_authentication(expected_users=0)
