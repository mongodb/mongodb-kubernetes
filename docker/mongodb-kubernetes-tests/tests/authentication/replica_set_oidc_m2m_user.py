import kubetester.oidc as oidc
import pytest
from kubetester import try_load, wait_until, find_fixture
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongodb_user import MongoDBUser
from kubetester.mongotester import ReplicaSetTester
from pytest import fixture

MDB_RESOURCE = "oidc-replica-set"
TEST_DATABASE = "myDB"


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("oidc/replica-set-user-id.yaml"), namespace=namespace)
    if try_load(resource):
        return resource

    oidc_provider_configs = resource.get_oidc_provider_configs()
    oidc_provider_configs[0]["clientId"] = oidc.get_cognito_workload_client_id()
    oidc_provider_configs[0]["audience"] = oidc.get_cognito_workload_client_id()
    oidc_provider_configs[0]["issuerURI"] = oidc.get_cognito_workload_url()

    resource.set_oidc_provider_configs(oidc_provider_configs)

    resource.set_version(ensure_ent_version(custom_mdb_version))

    return resource.update()


@fixture(scope="module")
def oidc_user(namespace) -> MongoDBUser:
    """Creates a password secret and then the user referencing it"""
    resource = MongoDBUser.from_yaml(find_fixture("oidc/oidc-user.yaml"), namespace=namespace)
    if try_load(resource):
        return resource

    resource["spec"]["username"] = f"OIDC-test-user/{oidc.get_cognito_workload_user_id()}"

    return resource.update()


@pytest.mark.e2e_replica_set_oidc
class TestCreateOIDCReplicaset(KubernetesTester):

    def test_create_replicaset(self, replica_set: MongoDB):
        replica_set.assert_reaches_phase(Phase.Running, timeout=400)

    def test_create_user(self, oidc_user: MongoDBUser):
        oidc_user.assert_reaches_phase(Phase.Updated, timeout=400)

    def test_assert_connectivity(self, replica_set: MongoDB):
        tester = replica_set.tester()
        tester.assert_oidc_authentication()

    def test_ops_manager_state_updated_correctly(self, replica_set: MongoDB):
        tester = replica_set.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled("MONGODB-OIDC", active_auth_mechanism=False)
        tester.assert_authentication_enabled(2)
        tester.assert_oidc_providers_size(1)
        tester.assert_expected_users(1)
        tester.assert_authoritative_set(True)


class TestNewUserAdditionToReplicaSet(KubernetesTester):
    def test_add_oidc_user(self, replica_set: MongoDB, namespace: str):
        replica_set.assert_reaches_phase(Phase.Running, timeout=400)

        replica_set.load()

        new_oidc_user = MongoDBUser.from_yaml(find_fixture("oidc/oidc-user.yaml"), namespace=namespace)
        new_oidc_user["metadata"]["name"] = "new-oidc-user"
        new_oidc_user["spec"]["username"] = "OIDC-test-user/dummy-user-id"

        new_oidc_user.update()
        new_oidc_user.assert_reaches_phase(Phase.Updated, timeout=400)

    def test_confirm_number_of_users(self, replica_set: MongoDB):
        def assert_expected_users() -> bool:
            tester = replica_set.get_automation_config_tester()
            try:
                tester.assert_expected_users(2)
                return True
            except AssertionError:
                return False

        wait_until(assert_expected_users, timeout=300, sleep=5)


@pytest.mark.e2e_replica_set_oidc
class TestRestrictedAccessToReplicaSet(KubernetesTester):
    def test_update_oidc_user(self, replica_set: MongoDB, oidc_user: MongoDBUser, namespace: str):
        oidc_user.load()
        oidc_user["spec"]["roles"] = [{"db": TEST_DATABASE, "name": "readWrite"}]
        oidc_user.update()

        oidc_user.assert_reaches_phase(Phase.Updated, timeout=400)

    def test_connection_with_specific_database(self, replica_set: MongoDB, oidc_user: MongoDBUser):
        replica_set.assert_reaches_phase(Phase.Running, timeout=400)
        tester = replica_set.tester()

        tester.assert_oidc_authentication(db=TEST_DATABASE)

    def test_connection_should_fail_with_other_database(self, replica_set: MongoDB, oidc_user: MongoDBUser):
        replica_set.assert_reaches_phase(Phase.Running, timeout=400)
        tester = replica_set.tester()

        tester.assert_oidc_authentication_fails()
