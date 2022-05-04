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

    def test_invalid_cluster_names(
        self, central_cluster_client: kubernetes.client.ApiClient
    ):
        resource = yaml.safe_load(open(yaml_fixture("mongodb-multi-cluster.yaml")))
        self.create_custom_resource_from_object(
            self.get_namespace(),
            resource,
            exception_reason="Cluster foo.mongokubernetes.com credentials is not specified in Kubeconfig",
            api_client=central_cluster_client,
        )
