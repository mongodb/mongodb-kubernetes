import pytest
from kubetester import (
    create_or_update_secret,
    read_configmap,
    try_load,
)
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.phase import Phase

from .replica_set_switch_project_helper import (
    ReplicaSetCreationAndProjectSwitchTestHelper,
)

# Constants
MDB_RESOURCE_NAME = "replica-set-scram-sha-256-switch-project"


@pytest.fixture(scope="module")
def replica_set(namespace: str) -> MongoDB:
    """
    Fixture to initialize the MongoDB resource for the replica set.

    Dynamically updates the resource configuration based on the test context.
    """
    resource = MongoDB.from_yaml(
        load_fixture("replica-set-scram-sha-256.yaml"), name=MDB_RESOURCE_NAME, namespace=namespace
    )

    if try_load(resource):
        return resource

    return resource.update()


@pytest.fixture(scope="module")
def test_helper(replica_set: MongoDB, namespace: str) -> ReplicaSetCreationAndProjectSwitchTestHelper:
    return ReplicaSetCreationAndProjectSwitchTestHelper(
        replica_set=replica_set, namespace=namespace, authentication_mechanism="SCRAM-SHA-256"
    )


@pytest.mark.e2e_replica_set_scram_sha_256_switch_project
class TestReplicaSetCreationAndProjectSwitch(KubernetesTester):
    """
    E2E test suite for replica set creation, user connectivity with SCRAM-SHA-256 authentication and switching Ops Manager project reference.
    """

    def test_create_replica_set(self, test_helper: ReplicaSetCreationAndProjectSwitchTestHelper):
        test_helper.test_create_replica_set()

    def test_replica_set_connectivity(self, test_helper: ReplicaSetCreationAndProjectSwitchTestHelper):
        test_helper.test_replica_set_connectivity(3)

    def test_ops_manager_state_correctly_updated(self, test_helper: ReplicaSetCreationAndProjectSwitchTestHelper):
        test_helper.test_ops_manager_state_with_expected_authentication(expected_users=0)

    def test_create_secret(self):
        create_or_update_secret(
            KubernetesTester.get_namespace(),
            "mms-user-1-password",
            {
                "password": "my-password",
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

    def test_ops_manager_state_with_users_correctly_updated(
        self, test_helper: ReplicaSetCreationAndProjectSwitchTestHelper
    ):
        user_name = "mms-user-1"
        expected_roles = {
            ("admin", "clusterAdmin"),
            ("admin", "userAdminAnyDatabase"),
            ("admin", "readWrite"),
            ("admin", "userAdminAnyDatabase"),
        }
        test_helper.test_ops_manager_state_with_users(
            user_name=user_name, expected_roles=expected_roles, expected_users=1
        )

    def test_switch_replica_set_project(
        self, test_helper: ReplicaSetCreationAndProjectSwitchTestHelper, namespace: str
    ):
        original_configmap = read_configmap(namespace=namespace, name="my-project")
        test_helper.test_switch_replica_set_project(
            original_configmap, new_project_configmap_name=namespace + "-" + "second"
        )

    def test_replica_set_connectivity_after_switch(self, test_helper: ReplicaSetCreationAndProjectSwitchTestHelper):
        test_helper.test_replica_set_connectivity(3)

    def test_ops_manager_state_correctly_updated_after_switch(
        self, test_helper: ReplicaSetCreationAndProjectSwitchTestHelper
    ):
        test_helper.test_ops_manager_state_with_expected_authentication(expected_users=1)

    def test_ops_manager_state_with_users_correctly_updated_after_switch(
        self, test_helper: ReplicaSetCreationAndProjectSwitchTestHelper
    ):
        user_name = "mms-user-1"
        expected_roles = {
            ("admin", "clusterAdmin"),
            ("admin", "userAdminAnyDatabase"),
            ("admin", "readWrite"),
            ("admin", "userAdminAnyDatabase"),
        }
        test_helper.test_ops_manager_state_with_users(
            user_name=user_name, expected_roles=expected_roles, expected_users=1
        )
