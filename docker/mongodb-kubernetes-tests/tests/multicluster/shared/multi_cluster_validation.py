import kubernetes
import yaml
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.operator import Operator


class TestWebhookValidation(KubernetesTester):
    def test_deploy_operator(multi_cluster_operator: Operator):
        multi_cluster_operator.assert_is_running()

    def test_unique_cluster_names(central_cluster_client: kubernetes.client.ApiClient, fixture: str):
        resource = yaml.safe_load(open(yaml_fixture(fixture)))
        resource["spec"]["clusterSpecList"].append({"clusterName": "kind-e2e-cluster-1", "members": 1})

        KubernetesTester.create_custom_resource_from_object(
            KubernetesTester.get_namespace(),
            resource,
            exception_reason="Multiple clusters with the same name (kind-e2e-cluster-1) are not allowed",
            api_client=central_cluster_client,
        )

    def test_only_one_schema(central_cluster_client: kubernetes.client.ApiClient, fixture: str):
        resource = yaml.safe_load(open(yaml_fixture(fixture)))
        resource["spec"]["cloudManager"] = {"configMapRef": {"name": " my-project"}}

        KubernetesTester.create_custom_resource_from_object(
            KubernetesTester.get_namespace(),
            resource,
            exception_reason="must validate one and only one schema",
            api_client=central_cluster_client,
        )

    def test_non_empty_clusterspec_list(central_cluster_client: kubernetes.client.ApiClient, fixture: str):
        resource = yaml.safe_load(open(yaml_fixture(fixture)))
        resource["spec"]["clusterSpecList"] = []

        KubernetesTester.create_custom_resource_from_object(
            KubernetesTester.get_namespace(),
            resource,
            exception_reason="ClusterSpecList empty is not allowed, please define at least one cluster",
            api_client=central_cluster_client,
        )
