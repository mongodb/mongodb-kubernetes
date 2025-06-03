import kubetester.oidc as oidc
import pytest
from kubetester import try_load, wait_until
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.mongotester import ReplicaSetTester
from kubetester.phase import Phase
from pytest import fixture

MDB_RESOURCE = "oidc-replica-set"


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("oidc/replica-set-workforce.yaml"), namespace=namespace)
    if try_load(resource):
        return resource

    oidc_provider_configs = resource.get_oidc_provider_configs()

    oidc_provider_configs[0]["clientId"] = oidc.get_cognito_workload_client_id()
    oidc_provider_configs[0]["audience"] = oidc.get_cognito_workload_client_id()
    oidc_provider_configs[0]["issuerURI"] = oidc.get_cognito_workload_url()

    resource.set_oidc_provider_configs(oidc_provider_configs)

    return resource.update()


@fixture(scope="module")
def oidc_user(namespace) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(load_fixture("oidc/oidc-user.yaml"), namespace=namespace)
    if try_load(resource):
        return resource

    resource["spec"]["username"] = f"OIDC-test-user/{oidc.get_cognito_workload_user_id()}"

    return resource.update()


@pytest.mark.e2e_replica_set_oidc_workforce
class TestCreateOIDCReplicaset(KubernetesTester):

    def test_create_replicaset(self, replica_set: MongoDB):
        replica_set.assert_reaches_phase(Phase.Running, timeout=400)

    def test_create_user(self, oidc_user: MongoDBUser):
        oidc_user.assert_reaches_phase(Phase.Updated, timeout=400)

    def test_ops_manager_state_updated_correctly(self, replica_set: MongoDB):
        tester = replica_set.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled("MONGODB-OIDC", active_auth_mechanism=False)
        tester.assert_authentication_enabled(2)
        tester.assert_oidc_providers_size(2)

        expected_oidc_configs = [
            {
                "audience": oidc.get_cognito_workload_client_id(),
                "issuerUri": oidc.get_cognito_workload_url(),
                "clientId": oidc.get_cognito_workload_client_id(),
                "userClaim": "sub",
                "groupsClaim": "cognito:groups",
                "JWKSPollSecs": 0,
                "authNamePrefix": "OIDC-test-group",
                "supportsHumanFlows": True,
                "useAuthorizationClaim": True,
            },
            {
                "audience": "dummy-audience",
                "issuerUri": "https://valid-issuer.example.com",
                "clientId": "dummy-client-id",
                "userClaim": "sub",
                "JWKSPollSecs": 0,
                "authNamePrefix": "OIDC-test-user",
                "supportsHumanFlows": False,
                "useAuthorizationClaim": False,
            },
        ]

        tester.assert_oidc_configuration(expected_oidc_configs)

        tester.assert_expected_users(1)
        tester.assert_authoritative_set(True)
