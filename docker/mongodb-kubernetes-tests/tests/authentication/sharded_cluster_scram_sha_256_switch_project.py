import pytest  
from kubetester.automation_config_tester import AutomationConfigTester  
from kubetester.kubetester import KubernetesTester  
from kubetester.kubetester import fixture as load_fixture  
from kubetester.mongodb import MongoDB  
from kubetester.mongotester import ShardedClusterTester  
from kubetester.phase import Phase  
  
from kubetester import (  
    read_configmap,  
    random_k8s_name,  
    create_or_update_configmap,  
)  
  
MDB_RESOURCE_NAME = "sharded-cluster-scram-sha-256-switch-project"
MDB_RESOURCE_BEFORE = "sharded-cluster-scram-sha-256-switch-project-before"  
MDB_RESOURCE_AFTER = "sharded-cluster-scram-sha-256-switch-project-after"  
  
@pytest.fixture(scope="module")  
def project_name_prefix(namespace: str) -> str:  
    """Generates a random Kubernetes project name prefix based on the namespace."""  
    return random_k8s_name(f"{namespace}-project-")  
  
  
@pytest.fixture(scope="module")  
def second_project(namespace: str, project_name_prefix: str) -> str:  
    """Creates or updates a ConfigMap for the second project."""  
    cm = read_configmap(namespace=namespace, name="my-project")  
    project_name = f"{project_name_prefix}-second"  
    return create_or_update_configmap(  
        namespace=namespace,  
        name=project_name,  
        data={  
            "baseUrl": cm["baseUrl"],  
            "projectName": project_name,  
            "orgId": cm["orgId"],  
        },  
    )  
  
@pytest.fixture(scope="module")  
def sharded_cluster(namespace: str) -> MongoDB:  
    """Fixture to initialize the MongoDB resource for the sharded cluster before the project switch."""  
    resource = MongoDB.from_yaml(load_fixture(f"{MDB_RESOURCE_BEFORE}.yaml"), namespace=namespace) 
    return resource  
  

@pytest.fixture(scope="module")  
def second_sharded_cluster(namespace: str, second_project: str) -> MongoDB:  
    """Fixture to initialize the MongoDB resource for the sharded cluster after the project switch."""  
    resource = MongoDB.from_yaml(load_fixture(f"{MDB_RESOURCE_AFTER}.yaml"), namespace=namespace)  
    resource["spec"]["opsManager"]["configMapRef"]["name"] = second_project  
    return resource  
  
  
@pytest.mark.e2e_sharded_cluster_scram_sha_256_switch_project  
class TestShardedClusterCreationAndProjectSwitch(KubernetesTester):  
    """  
    description: |  
      Creates a Sharded Cluster which switches the project and checks everything is created as expected.  
    """  
  
    def test_create_sharded_cluster(self, custom_mdb_version: str, sharded_cluster: MongoDB):  
        sharded_cluster.set_version(custom_mdb_version)  
        sharded_cluster.update()  

        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=600)  
  
    def test_sharded_cluster_connectivity(self):  
        ShardedClusterTester(MDB_RESOURCE_NAME, 1).assert_connectivity()  
  
    def test_ops_manager_state_correctly_updated_in_original_one(self, sharded_cluster: MongoDB):  
        tester = sharded_cluster.get_automation_config_tester()  
        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")  
        tester.assert_authentication_enabled()  
  
    def test_move_sharded_cluster(self, custom_mdb_version: str, second_sharded_cluster: MongoDB):  
        second_sharded_cluster.set_version(custom_mdb_version)  
        second_sharded_cluster.update()  
  
        second_sharded_cluster.assert_reaches_phase(Phase.Running, timeout=600)  
  
    def test_moved_sharded_cluster_connectivity(self):  
        ShardedClusterTester(MDB_RESOURCE_NAME, 1).assert_connectivity()  
  
    def test_ops_manager_state_correctly_updated_in_moved_one(self, second_sharded_cluster: MongoDB):  
        tester = second_sharded_cluster.get_automation_config_tester()  
        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")  
        tester.assert_authentication_enabled()  

