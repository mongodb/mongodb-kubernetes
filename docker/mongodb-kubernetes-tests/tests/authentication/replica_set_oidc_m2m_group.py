import pytest
from kubetester import try_load, wait_until
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongotester import ReplicaSetTester
from pytest import fixture

MDB_RESOURCE = "oidc-replica-set"


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("oidc/replica-set.yaml"), namespace=namespace)
    if try_load(resource):
        return resource

    resource.set_version(ensure_ent_version(custom_mdb_version))

    return resource.update()


@pytest.mark.e2e_replica_set_oidc
class TestCreateOIDCReplicaset(KubernetesTester):

    def test_create_replicaset(self, replica_set: MongoDB):
        replica_set.assert_reaches_phase(Phase.Running, timeout=400)

    def test_assert_connectivity(self, replica_set: MongoDB):
        tester = replica_set.tester()
        tester.assert_oidc_authentication()

    def test_ops_manager_state_updated_correctly(self, replica_set: MongoDB):
        tester = replica_set.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled("MONGODB-OIDC", active_auth_mechanism=False)
        tester.assert_authentication_enabled(2)

        tester.assert_expected_users(0)
        tester.assert_authoritative_set(True)


@pytest.mark.e2e_replica_set_oidc
class TestAddNewOIDCProvider(KubernetesTester):
    def test_add_oidc_provider_and_user(self, replica_set: MongoDB):
        replica_set.assert_reaches_phase(Phase.Running, timeout=400)

        replica_set.load()
        replica_set["spec"]["security"]["authentication"]["oidcProviderConfigs"] = [
            {
                "audience": "dummy-audience",
                "issuerURI": "<filled-in-test>",
                "requestedScopes": [],
                "userClaim": "sub",
                "groupsClaim": "cognito:groups",
                "authorizationMethod": "WorkloadIdentityFederation",
                "authorizationType": "GroupMembership",
                "configurationName": "dummy-oidc-config",
            }
        ]

        replica_set["spec"]["security"]["roles"] = [
            {
                "role": "dummy-oidc-config/test",
                "db": "admin",
                "roles": [{"role": "readWriteAnyDatabase", "db": "admin"}],
            }
        ]
        replica_set.update()

        def config_updated() -> bool:
            tester = replica_set.get_automation_config_tester()
            try:
                # Todo: add automation config update checks once the tests are working
                tester.assert_authentication_mechanism_enabled("MONGODB-OIDC", active_auth_mechanism=False)
                tester.assert_authentication_enabled(2)
                tester.assert_expected_users(0)
                # assert (config["oidcProviderConfigs"] == expectedConfig)
                return True
            except AssertionError:
                return False

        wait_until(config_updated, timeout=300, sleep=5)

        replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@pytest.mark.e2e_replica_set_oidc
class TestRoleChanges(KubernetesTester):
    def test_update_role(self, replica_set: MongoDB):
        replica_set.load()
        replica_set["spec"]["security"]["roles"] = [
            {
                "role": "dummy-oidc-config/test",
                "db": "admin",
                "roles": [
                    {"role": "readWriteAnyDatabase", "db": "admin"},
                    {"role": "clusterMonitor", "db": "admin"},
                ],
            }
        ]
        replica_set.update()

        def config_updated() -> bool:
            tester = replica_set.get_automation_config_tester()
            try:
                # Todo: add automation config update checks once the tests are working
                tester.assert_authentication_mechanism_enabled("MONGODB-OIDC", active_auth_mechanism=False)
                tester.assert_authentication_enabled(2)
                return True
            except AssertionError:
                return False

        wait_until(config_updated, timeout=300, sleep=5)

        replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@pytest.mark.e2e_replica_set_oidc
class TestOIDCRemoval(KubernetesTester):
    def test_remove_oidc_provider_and_user(self, replica_set: MongoDB):
        replica_set.assert_reaches_phase(Phase.Running, timeout=400)

        replica_set.load()
        replica_set["spec"]["security"]["authentication"]["modes"] = ["SCRAM"]
        replica_set["spec"]["security"]["authentication"]["oidcProviderConfigs"] = None
        replica_set["spec"]["security"]["roles"] = None
        replica_set.update()

        def config_updated() -> bool:
            tester = replica_set.get_automation_config_tester()
            try:
                tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256", active_auth_mechanism=False)
                tester.assert_authentication_enabled(1)
                return True
            except AssertionError:
                return False

        wait_until(config_updated, timeout=300, sleep=5)

        replica_set.assert_reaches_phase(Phase.Running, timeout=400)
