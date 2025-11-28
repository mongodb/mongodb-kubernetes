import pytest
from kubetester import (
    create_or_update_secret,
    try_load,
)
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.phase import Phase

from .helper_sharded_cluster_switch_project import (
    ShardedClusterSwitchProjectHelper,
)

MDB_RESOURCE_NAME = "sharded-cluster-scram-sha-1-switch-project"


@pytest.fixture(scope="function")
def sharded_cluster(namespace: str) -> MongoDB:

    resource = MongoDB.from_yaml(
        load_fixture("sharded-cluster-explicit-scram-sha-1.yaml"), name=MDB_RESOURCE_NAME, namespace=namespace
    )

    if try_load(resource):
        return resource

    return resource.update()


@pytest.fixture(scope="function")
def testhelper(sharded_cluster: MongoDB, namespace: str) -> ShardedClusterSwitchProjectHelper:
    return ShardedClusterSwitchProjectHelper(
        sharded_cluster=sharded_cluster,
        namespace=namespace,
        authentication_mechanism="MONGODB-CR",
        expected_num_deployment_auth_mechanisms=2,
    )


@pytest.mark.e2e_sharded_cluster_scram_sha_1_switch_project
class TestShardedClusterCreationAndProjectSwitch(KubernetesTester):
    """
    E2E test suite for sharded cluster creation, user connectivity with SCRAM-SHA-1 authentication and switching Ops Manager project reference.
    """

    PASSWORD_SECRET_NAME = "mms-user-1-password"
    USER_PASSWORD = "my-password"
    USER_NAME = "mms-user-1"

    def test_create_sharded_cluster(self, testhelper: ShardedClusterSwitchProjectHelper):
        testhelper.test_create_sharded_cluster()

    def test_sharded_cluster_connectivity(self, testhelper: ShardedClusterSwitchProjectHelper):
        testhelper.test_sharded_cluster_connectivity(1)

    def test_ops_manager_state_correctly_updated_in_initial_sharded_cluster(
        self, testhelper: ShardedClusterSwitchProjectHelper
    ):
        testhelper.test_ops_manager_state_with_expected_authentication(expected_users=0)

    # TODO CLOUDP-349093 - Disabled these tests because project migrations are not supported yet, which could lead to flaky behavior.
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

    # def test_ops_manager_state_with_users_correctly_updated(
    #     self, testhelper: ShardedClusterSwitchProjectHelper
    # ):
    #     user_name = "mms-user-1"
    #     expected_roles = {
    #         ("admin", "clusterAdmin"),
    #         ("admin", "userAdminAnyDatabase"),
    #         ("admin", "readWrite"),
    #         ("admin", "userAdminAnyDatabase"),
    #     }
    #     testhelper.test_ops_manager_state_with_users(
    #         user_name=user_name, expected_roles=expected_roles, expected_users=1
    #     )

    def test_switch_sharded_cluster_project(self, testhelper: ShardedClusterSwitchProjectHelper):
        testhelper.test_switch_sharded_cluster_project()

    def test_sharded_cluster_connectivity_after_switch(self, testhelper: ShardedClusterSwitchProjectHelper):
        testhelper.test_sharded_cluster_connectivity(1)

    def test_ops_manager_state_correctly_updated_after_switch(self, testhelper: ShardedClusterSwitchProjectHelper):
        testhelper.test_ops_manager_state_with_expected_authentication(expected_users=0)

    # TODO CLOUDP-349093 - Disabled these tests because project migrations are not supported yet, which could lead to flaky behavior.
    # def test_ops_manager_state_with_users_correctly_updated_after_switch(
    #     self, testhelper: ShardedClusterSwitchProjectHelper
    # ):
    #     user_name = "mms-user-1"
    #     expected_roles = {
    #         ("admin", "clusterAdmin"),
    #         ("admin", "userAdminAnyDatabase"),
    #         ("admin", "readWrite"),
    #         ("admin", "userAdminAnyDatabase"),
    #     }
    #     testhelper.test_ops_manager_state_with_users(
    #         user_name=user_name, expected_roles=expected_roles, expected_users=1
    #     )
