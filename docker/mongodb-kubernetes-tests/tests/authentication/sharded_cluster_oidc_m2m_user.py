import pytest
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester.utils import try_load
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongodb_user import MongoDBUser
from kubetester.mongotester import ShardedClusterTester
from pytest import fixture

MDB_RESOURCE = "oidc-sharded-cluster-replica-set"
USER_PASSWORD = "random"


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


@pytest.mark.e2e_sharded_cluster_oidc_m2m
class TestCreateOIDCShardedCluster(KubernetesTester):
    def test_create_sharded_cluster(self, custom_mdb_version: str):
        resource = MongoDB.from_yaml(load_fixture("oidc/sharded-cluster-m2m-user-id.yaml"), namespace=self.namespace)
        resource.set_version(ensure_ent_version(custom_mdb_version))
        resource.update()

        resource.assert_reaches_phase(Phase.Running)

    def test_create_user(self, oidc_user: MongoDBUser):
        oidc_user.assert_reaches_phase(Phase.Updated, timeout=400)

    def test_assert_connectivity(self):
        tester = ShardedClusterTester(MDB_RESOURCE, 2)
        tester.assert_oidc_authentication()

    def test_ops_manager_state_updated_correctly(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_enabled("MONGODB-OIDC", active_auth_mechanism=False)
        tester.assert_authentication_enabled(2)
        tester.assert_oidc_providers_size(1)
        tester.assert_expected_users(1)
        tester.assert_authoritative_set(True)
