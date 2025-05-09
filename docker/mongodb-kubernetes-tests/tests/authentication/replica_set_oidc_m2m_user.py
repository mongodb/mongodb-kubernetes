import pytest
from kubetester import try_load, wait_until
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongodb_user import MongoDBUser
from kubetester.mongotester import ReplicaSetTester
from pytest import fixture

MDB_RESOURCE = "oidc-replica-set"
USER_PASSWORD = "password"


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("oidc/replica-set-user-id.yaml"), namespace=namespace)
    if try_load(resource):
        return resource

    resource.set_version(ensure_ent_version(custom_mdb_version))

    return resource.update()


@fixture(scope="module")
def oidc_user(namespace) -> MongoDBUser:
    """Creates a password secret and then the user referencing it"""
    resource = MongoDBUser.from_yaml(load_fixture("oidc/oidc-user.yaml"), namespace=namespace)
    if try_load(resource):
        return resource

    KubernetesTester.create_secret(
        KubernetesTester.get_namespace(),
        resource.get_secret_name(),
        {
            "password": USER_PASSWORD,
        },
    )

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
