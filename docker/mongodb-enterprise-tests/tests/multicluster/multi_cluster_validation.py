from typing import Dict, List

import kubernetes
import pytest
import yaml
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.operator import Operator
from kubetester.kubetester import fixture as yaml_fixture, KubernetesTester


@pytest.mark.e2e_multi_cluster_validation
class TestWebhookValidation(KubernetesTester):
    def test_deploy_operator(self, multi_cluster_operator: Operator):
        multi_cluster_operator.assert_is_running()

    def test_duplicate_cluster_names(self, central_cluster_client: kubernetes.client.ApiClient):
        resource = yaml.safe_load(open(yaml_fixture("mongodb-multi-cluster.yaml")))
        resource["spec"]["clusterSpecList"].append({"clusterName": "kind-e2e-cluster-1", "members": 1})

        self.create_custom_resource_from_object(
            self.get_namespace(),
            resource,
            exception_reason="Multiple clusters with the same name (kind-e2e-cluster-1) are not allowed",
            api_client=central_cluster_client,
        )

    def test_only_one_schema(self, central_cluster_client: kubernetes.client.ApiClient):
        resource = yaml.safe_load(open(yaml_fixture("mongodb-multi-cluster.yaml")))
        resource["spec"]["cloudManager"] = {"configMapRef": {"name": " my-project"}}

        self.create_custom_resource_from_object(
            self.get_namespace(),
            resource,
            exception_reason="must validate one and only one schema",
            api_client=central_cluster_client,
        )
