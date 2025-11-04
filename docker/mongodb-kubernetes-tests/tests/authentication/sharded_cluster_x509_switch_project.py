import pytest
from kubetester import (
    create_or_update_configmap,
    random_k8s_name,
    read_configmap,
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

CONFIG_MAP_KEYS = {
    "BASE_URL": "baseUrl",
    "PROJECT_NAME": "projectName",
    "ORG_ID": "orgId",
}

MDB_RESOURCE_NAME = "sharded-cluster-x509-switch-project"
MDB_FIXTURE_NAME = MDB_RESOURCE_NAME


@pytest.fixture(scope="module")
def project_name_prefix(namespace: str) -> str:
    """
    Generates a random Kubernetes project name prefix based on the namespace.

    Ensures test isolation in a multi-namespace test environment.
    """
    return random_k8s_name(f"{namespace}-project-")


@pytest.fixture(scope="module")
def sharded_cluster(namespace: str, server_certs: str, agent_certs: str, issuer_ca_configmap: str) -> MongoDB:
    """
    Fixture to initialize the MongoDB resource for the sharded cluster.

    Dynamically updates the resource Ops Manager reference based on the test context.
    """
    resource = MongoDB.from_yaml(load_fixture(f"switch-project/{MDB_FIXTURE_NAME}.yaml"), namespace=namespace)
    resource["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    return resource


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    create_sharded_cluster_certs(
        namespace,
        MDB_RESOURCE_NAME,
        shards=1,
        mongod_per_shard=1,
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

    def test_create_sharded_cluster(self, custom_mdb_version: str, sharded_cluster: MongoDB):
        """
        Test cluster creation ensuring resources are applied correctly and cluster reaches Running phase.
        """
        sharded_cluster.set_version(custom_mdb_version)
        sharded_cluster.update()
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=600)

    def test_ops_manager_state_correctly_updated_in_initial_cluster(self, sharded_cluster: MongoDB):
        """
        Ensure Ops Manager state is correctly updated in the original cluster.
        """
        tester = sharded_cluster.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled("MONGODB-X509")
        tester.assert_authoritative_set(True)
        tester.assert_authentication_enabled()
        tester.assert_expected_users(0)

    def test_switch_sharded_cluster_project(
        self, custom_mdb_version: str, sharded_cluster: MongoDB, namespace: str, project_name_prefix: str
    ):
        """
        Modify the sharded cluster to switch its Ops Manager reference to a new project and verify lifecycle.
        """
        original_configmap = read_configmap(namespace=namespace, name="my-project")
        new_project_name = f"{project_name_prefix}-second"
        new_project_configmap = create_or_update_configmap(
            namespace=namespace,
            name=new_project_name,
            data={
                CONFIG_MAP_KEYS["BASE_URL"]: original_configmap[CONFIG_MAP_KEYS["BASE_URL"]],
                CONFIG_MAP_KEYS["PROJECT_NAME"]: new_project_name,
                CONFIG_MAP_KEYS["ORG_ID"]: original_configmap[CONFIG_MAP_KEYS["ORG_ID"]],
            },
        )

        sharded_cluster.load()
        sharded_cluster["spec"]["opsManager"]["configMapRef"]["name"] = new_project_configmap
        sharded_cluster.set_version(custom_mdb_version)
        sharded_cluster.update()

        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=600)

    def test_ops_manager_state_correctly_updated_in_moved_cluster(self, sharded_cluster: MongoDB):
        """
        Ensure Ops Manager state is correctly updated in the moved cluster after the project switch.
        """
        tester = sharded_cluster.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled("MONGODB-X509")
        tester.assert_authoritative_set(True)
        tester.assert_authentication_enabled()
        tester.assert_expected_users(0)
