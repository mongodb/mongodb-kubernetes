import kubernetes
import kubetester.oidc as oidc
import pytest
from kubetester import try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.operator import Operator
from pytest import fixture

from ..shared import multi_cluster_oidc_m2m_group as testhelper

MDB_RESOURCE = "oidc-multi-replica-set"


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names,
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("oidc/mongodbmulticluster-multi-m2m-group.yaml"), MDB_RESOURCE, namespace
    )
    if try_load(resource):
        return resource

    oidc_provider_configs = resource.get_oidc_provider_configs()

    oidc_provider_configs[0]["clientId"] = oidc.get_cognito_workload_client_id()
    oidc_provider_configs[0]["audience"] = oidc.get_cognito_workload_client_id()
    oidc_provider_configs[0]["issuerURI"] = oidc.get_cognito_workload_url()

    resource.set_oidc_provider_configs(oidc_provider_configs)

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    return resource.update()


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_oidc_m2m_group
class TestOIDCMultiCluster(KubernetesTester):
    def test_deploy_operator(self, multi_cluster_operator: Operator):
        testhelper.TestOIDCMultiCluster.test_deploy_operator(self, multi_cluster_operator)

    def test_create_oidc_replica_set(self, mongodb_multi: MongoDBMulti):
        testhelper.TestOIDCMultiCluster.test_create_oidc_replica_set(self, mongodb_multi)

    def test_assert_connectivity(self, mongodb_multi: MongoDBMulti):
        testhelper.TestOIDCMultiCluster.test_assert_connectivity(self, mongodb_multi)

    def test_ops_manager_state_updated_correctly(self, mongodb_multi: MongoDBMulti):
        testhelper.TestOIDCMultiCluster.test_ops_manager_state_updated_correctly(self, mongodb_multi)
