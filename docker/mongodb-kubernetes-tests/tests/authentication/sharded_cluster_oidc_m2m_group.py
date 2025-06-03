import kubetester.oidc as oidc
import pytest
from kubetester import find_fixture, try_load, wait_until
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import is_multi_cluster, skip_if_multi_cluster
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongotester import ShardedClusterTester
from pytest import fixture
from tests.shardedcluster.conftest import (
    enable_multi_cluster_deployment,
    get_mongos_service_names,
)

MDB_RESOURCE = "oidc-sharded-cluster-replica-set"


@fixture(scope="module")
def sharded_cluster(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("oidc/sharded-cluster-replica-set.yaml"), namespace=namespace)
    if try_load(resource):
        return resource

    oidc_provider_configs = resource.get_oidc_provider_configs()

    oidc_provider_configs[0]["clientId"] = oidc.get_cognito_workload_client_id()
    oidc_provider_configs[0]["audience"] = oidc.get_cognito_workload_client_id()
    oidc_provider_configs[0]["issuerURI"] = oidc.get_cognito_workload_url()

    resource.set_oidc_provider_configs(oidc_provider_configs)

    if is_multi_cluster():
        enable_multi_cluster_deployment(
            resource=resource,
            shard_members_array=[1, 1, 1],
            mongos_members_array=[1, 1, None],
            configsrv_members_array=[1, 1, 1],
        )

    return resource.update()


@pytest.mark.e2e_sharded_cluster_oidc_m2m_group
class TestCreateOIDCShardedCluster(KubernetesTester):

    def test_create_sharded_cluster(self, sharded_cluster: MongoDB):
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=800)

    def test_assert_connectivity(self, sharded_cluster: MongoDB):
        service_names = None
        if is_multi_cluster():
            service_names = get_mongos_service_names(sharded_cluster)
        tester = sharded_cluster.tester(service_names=service_names)
        tester.assert_oidc_authentication()

    def test_ops_manager_state_updated_correctly(self, sharded_cluster: MongoDB):
        tester = sharded_cluster.get_automation_config_tester()

        tester.assert_authentication_mechanism_enabled("MONGODB-OIDC", active_auth_mechanism=False)
        tester.assert_authentication_enabled(2)
        tester.assert_oidc_providers_size(2)
        tester.assert_expected_users(0)
        tester.assert_has_expected_number_of_roles(expected_roles=1)
        tester.assert_authoritative_set(True)

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
                "audience": "test-audience",
                "issuerUri": "https://valid-issuer-1.example.com",
                "clientId": "test-client-id",
                "userClaim": "sub",
                "JWKSPollSecs": 0,
                "authNamePrefix": "OIDC-test-user",
                "supportsHumanFlows": True,
                "useAuthorizationClaim": False,
            },
        ]
        tester.assert_oidc_configuration(expected_oidc_configs)


@skip_if_multi_cluster()
@pytest.mark.e2e_sharded_cluster_oidc_m2m_group
class TestAddNewOIDCProviderAndRole(KubernetesTester):
    def test_add_oidc_provider_and_role(self, sharded_cluster: MongoDB):
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=400)

        sharded_cluster.load()

        new_oidc_provider_config = {
            "audience": "dummy-audience",
            "issuerURI": "https://valid-issuer-2.example.com",
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

        sharded_cluster.append_oidc_provider_config(new_oidc_provider_config)
        sharded_cluster.append_role(new_role)

        sharded_cluster.update()
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=400)

        def config_and_roles_preserved() -> bool:
            tester = sharded_cluster.get_automation_config_tester()
            try:
                tester.assert_authentication_mechanism_enabled("MONGODB-OIDC", active_auth_mechanism=False)
                tester.assert_authentication_enabled(2)
                tester.assert_expected_users(0)
                tester.assert_has_expected_number_of_roles(expected_roles=2)
                tester.assert_oidc_providers_size(3)

                # Updated configuration check with all 3 providers
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
                        "audience": "test-audience",
                        "issuerUri": "https://valid-issuer-1.example.com",
                        "clientId": "test-client-id",
                        "userClaim": "sub",
                        "JWKSPollSecs": 0,
                        "authNamePrefix": "OIDC-test-user",
                        "supportsHumanFlows": True,
                        "useAuthorizationClaim": False,
                    },
                    {
                        "audience": "dummy-audience",
                        "issuerUri": "https://valid-issuer-2.example.com",
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

        wait_until(config_and_roles_preserved, timeout=600, sleep=5)
