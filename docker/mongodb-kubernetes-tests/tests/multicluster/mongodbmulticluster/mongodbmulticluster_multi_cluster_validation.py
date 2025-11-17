import kubernetes
import pytest
import yaml
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.operator import Operator

from ..shared import multi_cluster_validation as testhelper

MDBM_RESOURCE = "mongodbmulticluster-multi-cluster.yaml"


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_validation
class TestWebhookValidation(KubernetesTester):
    def test_deploy_operator(self, multi_cluster_operator: Operator):
        testhelper.TestWebhookValidation.test_deploy_operator(multi_cluster_operator, MDBM_RESOURCE)

    def test_unique_cluster_names(self, central_cluster_client: kubernetes.client.ApiClient):
        testhelper.TestWebhookValidation.test_unique_cluster_names(central_cluster_client, MDBM_RESOURCE)

    def test_only_one_schema(self, central_cluster_client: kubernetes.client.ApiClient):
        testhelper.TestWebhookValidation.test_only_one_schema(central_cluster_client, MDBM_RESOURCE)

    def test_non_empty_clusterspec_list(self, central_cluster_client: kubernetes.client.ApiClient):
        testhelper.TestWebhookValidation.test_non_empty_clusterspec_list(central_cluster_client, MDBM_RESOURCE)
