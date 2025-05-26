import kubetester.oidc as oidc
import pytest
from kubetester import find_fixture, try_load, wait_until
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongotester import ReplicaSetTester
from pytest import fixture

MDB_RESOURCE = "oidc-replica-set"


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("oidc/replica-set.yaml"), namespace=namespace)
    if try_load(resource):
        return resource

    oidc_provider_configs = resource.get_oidc_provider_configs()
    oidc_provider_configs[0]["clientId"] = oidc.get_cognito_workload_client_id()
    oidc_provider_configs[0]["audience"] = oidc.get_cognito_workload_client_id()
    oidc_provider_configs[0]["issuerURI"] = oidc.get_cognito_workload_url()

    resource.set_oidc_provider_configs(oidc_provider_configs)

    return resource.update()


# Tests that one Workload Group membership works as expected.
@pytest.mark.e2e_replica_set_oidc_m2m_group
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


# Adds a second workload group membership and associated role; automation config is verified
@pytest.mark.e2e_replica_set_oidc_m2m_group
class TestAddNewOIDCProviderAndRole(KubernetesTester):
    def test_add_oidc_provider_and_role(self, replica_set: MongoDB):
        replica_set.assert_reaches_phase(Phase.Running, timeout=400)

        replica_set.load()

        new_oidc_provider_config = {
            "audience": "dummy-audience",
            "issuerURI": "https://valid-issuer.example.com",
            "requestedScopes": [],
            "userClaim": "sub",
            "groupsClaim": "group",
            "authorizationMethod": "WorkloadIdentityFederation",
            "authorizationType": "GroupMembership",
            "configurationName": "dummy-oidc-config",
        }

        new_role = {
            "role": "dummy-oidc-config/test",
            "db": "admin",
            "roles": [{"role": "readWriteAnyDatabase", "db": "admin"}],
        }

        replica_set.append_oidc_provider_config(new_oidc_provider_config)
        replica_set.append_role(new_role)

        replica_set.update()
        replica_set.assert_reaches_phase(Phase.Running, timeout=400)

        def config_and_roles_preserved() -> bool:
            tester = replica_set.get_automation_config_tester()
            try:

                tester.assert_authentication_mechanism_enabled("MONGODB-OIDC", active_auth_mechanism=False)
                tester.assert_authentication_enabled(2)
                tester.assert_expected_users(0)
                tester.assert_has_expected_number_of_roles(expected_roles=2)

                expected_oidc_configs = [
                    {
                        "audience": oidc.get_cognito_workload_client_id(),
                        "issuerUri": oidc.get_cognito_workload_url(),
                        "clientId": oidc.get_cognito_workload_client_id(),
                        "userClaim": "sub",
                        "groupsClaim": "cognito:groups",
                        "JWKSPollSecs": 0,
                        "authNamePrefix": "OIDC-test",
                        "supportsHumanFlows": False,
                        "useAuthorizationClaim": True,
                    },
                    {
                        "audience": "dummy-audience",
                        "issuerUri": "https://valid-issuer.example.com",
                        "userClaim": "sub",
                        "groupsClaim": "group",
                        "JWKSPollSecs": 0,
                        "authNamePrefix": "dummy-oidc-config",
                        "supportsHumanFlows": False,
                        "useAuthorizationClaim": True,
                    },
                ]

                tester.assert_oidc_configuration(expected_oidc_configs)
                return True
            except AssertionError:
                return False

        wait_until(config_and_roles_preserved, timeout=300, sleep=5)


# Tests the removal of all oidc configs and roles
@pytest.mark.e2e_replica_set_oidc_m2m_group
class TestOIDCRemoval(KubernetesTester):
    def test_remove_oidc_provider_and_user(self, replica_set: MongoDB):
        replica_set.assert_reaches_phase(Phase.Running, timeout=400)

        replica_set.load()
        replica_set["spec"]["security"]["authentication"]["modes"] = ["SCRAM"]
        replica_set["spec"]["security"]["authentication"]["oidcProviderConfigs"] = None
        replica_set["spec"]["security"]["roles"] = None

        replica_set.update()
        replica_set.assert_reaches_phase(Phase.Running, timeout=400)

        def config_updated() -> bool:
            tester = replica_set.get_automation_config_tester()
            try:
                tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256", active_auth_mechanism=False)
                tester.assert_authentication_enabled(1)
                return True
            except AssertionError:
                return False

        wait_until(config_updated, timeout=300, sleep=5)
