import kubernetes
import pytest
import yaml
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.operator import Operator


@pytest.mark.e2e_multi_cluster_validation_webhook
class TestWebhookValidation(KubernetesTester):
    def test_deploy_operator(self, multi_cluster_operator: Operator):
        multi_cluster_operator.wait_for_operator_ready()

    def test_unique_cluster_names(self, central_cluster_client: kubernetes.client.ApiClient):
        resource = yaml.safe_load(open(yaml_fixture("mongodb-multi-cluster.yaml")))
        resource["spec"]["clusterSpecList"].append({"clusterName": "kind-e2e-cluster-1", "members": 1})

        self.create_custom_resource_from_object(
            self.get_namespace(),
            resource,
            exception_reason="Multiple clusters with the same name (kind-e2e-cluster-1) are not allowed",
            api_client=central_cluster_client,
        )
