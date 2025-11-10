import pytest
from kubetester import (
    create_or_update_configmap,
    create_or_update_secret,
    read_configmap,
    try_load,
)
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.mongotester import ShardedClusterTester
from kubetester.phase import Phase

MDB_RESOURCE_NAME = "sharded-cluster-scram-sha-256-switch-project"


@pytest.fixture(scope="module")
def sharded_cluster(namespace: str) -> MongoDB:
    """
    Fixture to initialize the MongoDB resource for the sharded cluster.

    Dynamically updates the resource Ops Manager reference based on the test context.
    """
    resource = MongoDB.from_yaml(
        load_fixture("sharded-cluster-scram-sha-256.yaml"), name=MDB_RESOURCE_NAME, namespace=namespace
    )

    if try_load(resource):
        return resource

    return resource.update()


@pytest.mark.e2e_sharded_cluster_scram_sha_256_switch_project
class TestShardedClusterCreationAndProjectSwitch(KubernetesTester):
    """
    E2E test suite for sharded cluster creation, user connectivity with SCRAM-SHA-256 authentication and switching Ops Manager project reference.
    """

    PASSWORD_SECRET_NAME = "mms-user-1-password"
    USER_PASSWORD = "my-password"
    USER_NAME = "mms-user-1"

    def test_create_sharded_cluster(self, sharded_cluster: MongoDB):
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=600)

    def test_sharded_cluster_connectivity(self):
        ShardedClusterTester(MDB_RESOURCE_NAME, 1).assert_connectivity()

    def test_ops_manager_state_correctly_updated_in_initial_cluster(self, sharded_cluster: MongoDB):
        tester = sharded_cluster.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
        tester.assert_authentication_enabled()

    # def test_create_secret(self):
    #     create_or_update_secret(
    #         KubernetesTester.get_namespace(),
    #         self.PASSWORD_SECRET_NAME,
    #         {
    #             "password": self.USER_PASSWORD,
    #         },
    #     )

    # def test_create_user(self, namespace: str):
    #     mdb = MongoDBUser.from_yaml(
    #         load_fixture("scram-sha-user.yaml"),
    #         namespace=namespace,
    #     )
    #     mdb["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME

    #     mdb.update()
    #     mdb.assert_reaches_phase(Phase.Updated, timeout=150)

    # def test_ops_manager_state_with_users_correctly_updated(self, sharded_cluster: MongoDB):
    #     expected_roles = {
    #         ("admin", "clusterAdmin"),
    #         ("admin", "userAdminAnyDatabase"),
    #         ("admin", "readWrite"),
    #         ("admin", "userAdminAnyDatabase"),
    #     }

    #     tester = sharded_cluster.get_automation_config_tester()
    #     tester.assert_has_user(self.USER_NAME)
    #     tester.assert_user_has_roles(self.USER_NAME, expected_roles)
    #     tester.assert_expected_users(1)

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

        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=800)

    def test_moved_sharded_cluster_connectivity(self):
        ShardedClusterTester(MDB_RESOURCE_NAME, 1).assert_connectivity()

    def test_ops_manager_state_correctly_updated_in_moved_cluster(self, sharded_cluster: MongoDB):
        tester = sharded_cluster.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
        tester.assert_authentication_enabled()

    # def test_ops_manager_state_with_users_correctly_updated_after_switch(self, sharded_cluster: MongoDB):
    #     expected_roles = {
    #         ("admin", "clusterAdmin"),
    #         ("admin", "userAdminAnyDatabase"),
    #         ("admin", "readWrite"),
    #         ("admin", "userAdminAnyDatabase"),
    #     }

    #     tester = sharded_cluster.get_automation_config_tester()
    #     tester.assert_has_user(self.USER_NAME)
    #     tester.assert_user_has_roles(self.USER_NAME, expected_roles)
    #     tester.assert_expected_users(1)
