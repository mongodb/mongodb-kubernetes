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
from kubetester.mongotester import ReplicaSetTester
from kubetester.phase import Phase

# Constants
MDB_RESOURCE_NAME = "replica-set-scram-sha-1-switch-project"


@pytest.fixture(scope="module")
def replica_set(namespace: str) -> MongoDB:
    """
    Fixture to initialize the MongoDB resource for the replica set.

    Dynamically updates the resource configuration based on the test context.
    """
    resource = MongoDB.from_yaml(
        load_fixture("replica-set-explicit-scram-sha-1.yaml"), name=MDB_RESOURCE_NAME, namespace=namespace
    )

    if try_load(resource):
        return resource

    return resource.update()


@pytest.mark.e2e_replica_set_scram_sha_1_switch_project
class TestReplicaSetCreationAndProjectSwitch(KubernetesTester):
    """
    E2E test suite for replica set creation, user connectivity with SCRAM-SHA-1 authentication and switching Ops Manager project reference.
    """

    PASSWORD_SECRET_NAME = "mms-user-1-password"
    USER_PASSWORD = "my-password"
    USER_NAME = "mms-user-1"

    def test_create_replica_set(self, replica_set: MongoDB):
        replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    def test_replica_set_connectivity(self):
        ReplicaSetTester(MDB_RESOURCE_NAME, 3).assert_connectivity()

    def test_ops_manager_state_correctly_updated_in_initial_replica_set(self, replica_set: MongoDB):
        tester = replica_set.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled("MONGODB-CR")
        tester.assert_authoritative_set(True)
        tester.assert_authentication_enabled(2)
        tester.assert_expected_users(0)

    def test_create_secret(self):
        create_or_update_secret(
            KubernetesTester.get_namespace(),
            self.PASSWORD_SECRET_NAME,
            {
                "password": self.USER_PASSWORD,
            },
        )

    def test_create_user(self, namespace: str):
        mdb = MongoDBUser.from_yaml(
            load_fixture("scram-sha-user.yaml"),
            namespace=namespace,
        )
        mdb["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME

        mdb.update()
        mdb.assert_reaches_phase(Phase.Updated, timeout=150)

    def test_ops_manager_state_with_users_correctly_updated(self, replica_set: MongoDB):
        expected_roles = {
            ("admin", "clusterAdmin"),
            ("admin", "userAdminAnyDatabase"),
            ("admin", "readWrite"),
            ("admin", "userAdminAnyDatabase"),
        }

        tester = replica_set.get_automation_config_tester()
        tester.assert_has_user(self.USER_NAME)
        tester.assert_user_has_roles(self.USER_NAME, expected_roles)
        tester.assert_expected_users(1)

    def test_switch_replica_set_project(self, replica_set: MongoDB, namespace: str):
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

        replica_set["spec"]["opsManager"]["configMapRef"]["name"] = new_project_configmap
        replica_set.update()

        replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    def test_moved_replica_set_connectivity(self):
        ReplicaSetTester(MDB_RESOURCE_NAME, 3).assert_connectivity()

    def test_ops_manager_state_correctly_updated_in_moved_replica_set(self, replica_set: MongoDB):
        tester = replica_set.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled("MONGODB-CR")
        tester.assert_authoritative_set(True)
        tester.assert_authentication_enabled(2)

    def test_ops_manager_state_with_users_correctly_updated_after_switch(self, replica_set: MongoDB):
        expected_roles = {
            ("admin", "clusterAdmin"),
            ("admin", "userAdminAnyDatabase"),
            ("admin", "readWrite"),
            ("admin", "userAdminAnyDatabase"),
        }

        tester = replica_set.get_automation_config_tester()
        tester.assert_has_user(self.USER_NAME)
        tester.assert_user_has_roles(self.USER_NAME, expected_roles)
        tester.assert_expected_users(1)
