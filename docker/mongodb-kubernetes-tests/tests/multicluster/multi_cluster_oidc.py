import kubernetes
import kubetester.oidc as oidc
import pytest
from kubetester import find_fixture, try_load, wait_until
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.mongotester import ReplicaSetTester
from kubetester.operator import Operator
from pytest import fixture

MDB_RESOURCE = "oidc-multi-replica-set"


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names,
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("oidc/mongodb-multi.yaml"), MDB_RESOURCE, namespace)
    if try_load(resource):
        return resource

    oidc_provider_configs = resource.get_oidc_provider_configs()

    oidc_provider_configs[0]["clientId"] = oidc.get_cognito_workload_client_id()
    oidc_provider_configs[0]["audience"] = oidc.get_cognito_workload_client_id()
    oidc_provider_configs[0]["issuerURI"] = oidc.get_cognito_workload_url()

    resource.set_oidc_provider_configs(oidc_provider_configs)

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    return resource.update()


@pytest.mark.e2e_multi_cluster_oidc
class TestOIDCMultiCluster(KubernetesTester):
    def test_deploy_operator(self, multi_cluster_operator: Operator):
        multi_cluster_operator.assert_is_running()

    def test_create_oidc_replica_set(self, mongodb_multi: MongoDBMulti):
        mongodb_multi.assert_reaches_phase(Phase.Running, timeout=800)

    def test_assert_connectivity(self, mongodb_multi: MongoDBMulti):
        tester = mongodb_multi.tester()
        tester.assert_oidc_authentication()

    def test_ops_manager_state_updated_correctly(self, mongodb_multi: MongoDBMulti):
        tester = mongodb_multi.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled("MONGODB-OIDC", active_auth_mechanism=False)
        tester.assert_authentication_enabled(2)
        tester.assert_expected_users(0)
        tester.assert_authoritative_set(True)
