import pytest
from kubetester import (
    create_or_update_configmap,
    read_configmap,
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
from kubetester.mongotester import ShardedClusterTester
from kubetester.phase import Phase

MDB_RESOURCE_NAME = "sharded-cluster-x509-switch-project"


@pytest.fixture(scope="module")
def sharded_cluster(namespace: str, server_certs: str, agent_certs: str, issuer_ca_configmap: str) -> MongoDB:
    """
    Fixture to initialize the MongoDB resource for the sharded cluster.

    Dynamically updates the resource Ops Manager reference based on the test context.
    """
    resource = MongoDB.from_yaml(
        load_fixture("sharded-cluster-x509-internal-cluster-auth-transition.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )

    if try_load(resource):
        return resource

    resource["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    return resource.update()


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    create_sharded_cluster_certs(
        namespace,
        MDB_RESOURCE_NAME,
        shards=2,
        mongod_per_shard=3,
        config_servers=1,
        mongos=1,
    )


@pytest.fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_x509_agent_tls_certs(issuer, namespace, MDB_RESOURCE_NAME)


@pytest.mark.e2e_sharded_cluster_x509_switch_project
class TestShardedClusterCreationAndProjectSwitch(KubernetesTester):
    """
    E2E test suite for sharded cluster creation, user connectivity with X509 authentication and switching Ops Manager project reference.
    """

    def test_create_sharded_cluster(self, sharded_cluster: MongoDB):
        sharded_cluster.update()
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=600)

    def test_ops_manager_state_correctly_updated_in_initial_cluster(self, sharded_cluster: MongoDB):
        tester = sharded_cluster.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled("MONGODB-X509")
        tester.assert_authoritative_set(True)
        tester.assert_authentication_enabled()
        tester.assert_expected_users(0)

    def test_switch_sharded_cluster_project(self, sharded_cluster: MongoDB, namespace: str):
        original_configmap = read_configmap(namespace=namespace, name="my-project")
        new_project_name = namespace + "-" + "second"
        new_project_configmap = create_or_update_configmap(
            namespace=namespace,
            name=new_project_name,
            data={
                "baseUrl": original_configmap["baseUrl"],
                "projectName": new_project_name,
                "orgId": original_configmap["orgId"],
            },
        )

        sharded_cluster["spec"]["opsManager"]["configMapRef"]["name"] = new_project_configmap
        sharded_cluster.update()

        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=600)

    def test_ops_manager_state_correctly_updated_in_moved_cluster(self, sharded_cluster: MongoDB):
        tester = sharded_cluster.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled("MONGODB-X509")
        tester.assert_authoritative_set(True)
        tester.assert_authentication_enabled()
        tester.assert_expected_users(0)
