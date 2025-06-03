import kubetester.oidc as oidc
import pytest
from kubetester import find_fixture, try_load
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import is_multi_cluster
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongodb_user import MongoDBUser
from kubetester.mongotester import ShardedClusterTester
from pytest import fixture
from tests.shardedcluster.conftest import (
    enable_multi_cluster_deployment,
    get_mongos_service_names,
)

MDB_RESOURCE = "oidc-sharded-cluster-replica-set"


@fixture(scope="module")
def sharded_cluster(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("oidc/sharded-cluster-m2m-user.yaml"), namespace=namespace)

    oidc_provider_configs = resource.get_oidc_provider_configs()

    oidc_provider_configs[0]["issuerURI"] = oidc.get_cognito_workload_url()
    oidc_provider_configs[0]["clientId"] = oidc.get_cognito_workload_client_id()
    oidc_provider_configs[0]["audience"] = oidc.get_cognito_workload_client_id()

    resource.set_oidc_provider_configs(oidc_provider_configs)

    if is_multi_cluster():
        enable_multi_cluster_deployment(
            resource=resource,
            shard_members_array=[1, 1, 1],
            mongos_members_array=[1, 1, None],
            configsrv_members_array=[1, 1, 1],
        )

    if try_load(resource):
        return resource

    return resource.update()


@fixture(scope="module")
def oidc_user(namespace) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(load_fixture("oidc/oidc-user.yaml"), namespace=namespace)

    resource["spec"]["username"] = f"OIDC-test-user/{oidc.get_cognito_workload_user_id()}"
    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE

    return resource.update()


@pytest.mark.e2e_sharded_cluster_oidc_m2m_user
class TestCreateOIDCShardedCluster(KubernetesTester):
    def test_create_sharded_cluster(self, sharded_cluster: MongoDB):
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=800)

    def test_create_user(self, oidc_user: MongoDBUser):
        oidc_user.assert_reaches_phase(Phase.Updated, timeout=400)

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
        tester.assert_oidc_providers_size(1)
        tester.assert_expected_users(1)
        tester.assert_authoritative_set(True)

        expected_oidc_configs = [
            {
                "audience": oidc.get_cognito_workload_client_id(),
                "issuerUri": oidc.get_cognito_workload_url(),
                "clientId": oidc.get_cognito_workload_client_id(),
                "userClaim": "sub",
                "JWKSPollSecs": 0,
                "authNamePrefix": "OIDC-test-user",
                "supportsHumanFlows": False,
                "useAuthorizationClaim": False,
            },
        ]
        tester.assert_oidc_configuration(expected_oidc_configs)
