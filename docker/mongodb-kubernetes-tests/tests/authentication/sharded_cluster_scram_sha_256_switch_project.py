import os

import pytest
from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.phase import Phase

from .helper_switch_project import SwitchProjectHelper

MDB_RESOURCE_NAME = "sharded-cluster-scram-sha-256-switch-project"

# Quarantined: blocked on an upstream Cloud Manager Automation Agent 13.53 regression that
# stalls mongos recovery after a sharded cluster project switch. See HELP-96015.
#
# To force-run these tests anyway (e.g. to check whether the upstream fix has landed):
#   - Locally: RUN_QUARANTINED_TESTS=true pytest -m e2e_sharded_cluster_scram_sha_256_switch_project ...
#   - Evergreen CLI: evergreen patch -p mongodb-kubernetes \
#       -t e2e_sharded_cluster_scram_sha_256_switch_project --param RUN_QUARANTINED_TESTS=true
#   - Evergreen UI: schedule the task, then set expansion RUN_QUARANTINED_TESTS=true
#     under "Configure Task" before running it.
QUARANTINE_REASON = (
    "Quarantined: blocked on upstream Cloud Manager Automation Agent 13.53 regression that "
    "stalls mongos recovery after a project switch. See https://jira.mongodb.org/browse/HELP-96015."
)
skip_quarantined = pytest.mark.skipif(
    os.environ.get("RUN_QUARANTINED_TESTS", "false").lower() != "true",
    reason=QUARANTINE_REASON,
)


@pytest.fixture(scope="function")
def sharded_cluster(namespace: str) -> MongoDB:

    resource = MongoDB.from_yaml(
        load_fixture("sharded-cluster-scram-sha-256.yaml"), name=MDB_RESOURCE_NAME, namespace=namespace
    )

    try_load(resource)
    return resource


@pytest.fixture(scope="function")
def testhelper(sharded_cluster: MongoDB, namespace: str) -> SwitchProjectHelper:
    return SwitchProjectHelper(
        resource=sharded_cluster,
        namespace=namespace,
        authentication_mechanism="SCRAM-SHA-256",
        expected_num_deployment_auth_mechanisms=1,
    )


@pytest.mark.e2e_sharded_cluster_scram_sha_256_switch_project
class TestShardedClusterCreationAndProjectSwitch(KubernetesTester):
    """
    E2E test suite for sharded cluster creation, user connectivity with SCRAM-SHA-256 authentication and switching Ops Manager project reference.
    """

    PASSWORD_SECRET_NAME = "mms-user-1-password"
    USER_PASSWORD = "my-password"
    USER_NAME = "mms-user-1"

    def test_create_sharded_cluster(self, testhelper: SwitchProjectHelper):
        testhelper.test_create_resource()

    def test_sharded_cluster_connectivity(self, testhelper: SwitchProjectHelper):
        testhelper.test_sharded_cluster_connectivity(1)

    def test_ops_manager_state_correctly_updated_in_initial_sharded_cluster(self, testhelper: SwitchProjectHelper):
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

    @skip_quarantined
    def test_switch_project(self, testhelper: SwitchProjectHelper):
        testhelper.test_switch_project()

    @skip_quarantined
    def test_sharded_cluster_connectivity_after_switch(self, testhelper: SwitchProjectHelper):
        testhelper.test_sharded_cluster_connectivity(1)

    @skip_quarantined
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
