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

from .helper_switch_project import (
    SwitchProjectHelper,
)

MDB_RESOURCE_NAME = "replica-set-scram-sha-1-switch-project"


@pytest.fixture(scope="function")
def replica_set(namespace: str) -> MongoDB:

    resource = MongoDB.from_yaml(
        load_fixture("replica-set-explicit-scram-sha-1.yaml"), name=MDB_RESOURCE_NAME, namespace=namespace
    )

    if try_load(resource):
        return resource

    return resource.update()


@pytest.fixture(scope="function")
def testhelper(replica_set: MongoDB, namespace: str) -> SwitchProjectHelper:
    return SwitchProjectHelper(
        resource=replica_set,
        namespace=namespace,
        authentication_mechanism="MONGODB-CR",
        expected_num_deployment_auth_mechanisms=2,
    )


@pytest.mark.e2e_replica_set_scram_sha_1_switch_project
class TestReplicaSetCreationAndProjectSwitch(KubernetesTester):
    """
    E2E test suite for replica set creation, user connectivity with SCRAM-SHA-1 authentication and switching Ops Manager project reference.
    """

    def test_create_resource(self, testhelper: SwitchProjectHelper):
        testhelper.test_create_resource()

    def test_replica_set_connectivity(self, testhelper: SwitchProjectHelper):
        testhelper.test_replica_set_connectivity(3)

    def test_ops_manager_state_correctly_updated_in_initial_replica_set(self, testhelper: SwitchProjectHelper):
        testhelper.test_ops_manager_state_with_expected_authentication(expected_users=0)

    # TODO CLOUDP-349093 - Disabled these tests because project migrations are not supported yet, which could lead to flaky behavior.
    # def test_create_secret(self):
    #     create_or_update_secret(
    #         KubernetesTester.get_namespace(),
    #         "mms-user-1-password",
    #         {
    #             "password": "my-password",
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
    #     self, testhelper: SwitchProjectHelper
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

    def test_switch_project(self, testhelper: SwitchProjectHelper):
        testhelper.test_switch_project()

    def test_replica_set_connectivity_after_switch(self, testhelper: SwitchProjectHelper):
        testhelper.test_replica_set_connectivity(3)

    def test_ops_manager_state_correctly_updated_after_switch(self, testhelper: SwitchProjectHelper):
        testhelper.test_ops_manager_state_with_expected_authentication(expected_users=0)

    # TODO CLOUDP-349093 - Disabled these tests because project migrations are not supported yet, which could lead to flaky behavior.
    # def test_ops_manager_state_with_users_correctly_updated_after_switch(
    #     self, testhelper: SwitchProjectHelper
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
