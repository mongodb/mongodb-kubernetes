from typing import Dict, List
from pytest import mark, fixture

import kubernetes
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.operator import Operator
from kubetester.kubetester import fixture as yaml_fixture
from kubernetes import client
from kubeobject import CustomObject


@mark.e2e_multi_cluster_fail_cluster_connectivity
def test_label_namespace(
    namespace: str, central_cluster_client: kubernetes.client.ApiClient
):

    api = client.CoreV1Api(api_client=central_cluster_client)

    labels = {"istio-injection": "enabled"}
    ns = api.read_namespace(name=namespace)

    ns.metadata.labels = labels
    api.replace_namespace(name=namespace, body=ns)


@mark.e2e_multi_cluster_fail_cluster_connectivity
def test_create_service_entry(
    namespace: str, central_cluster_client: kubernetes.client.ApiClient
):
    service_entry = CustomObject(
        name="cluster-block",
        namespace=namespace,
        kind="ServiceEntry",
        plural="serviceentries",
        group="networking.istio.io",
        version="v1beta1",
        api_client=central_cluster_client,
    )

    service_entry["spec"] = {
        "hosts": [
            "api.e2e.cluster1.mongokubernetes.com",
            "api.e2e.cluster2.mongokubernetes.com",
        ],
        "location": "MESH_EXTERNAL",
        "ports": [{"name": "https", "number": 443, "protocol": "HTTPS"}],
        "resolution": "DNS",
    }
    service_entry.api = kubernetes.client.CustomObjectsApi(
        api_client=central_cluster_client
    )
    service_entry.create()


@mark.e2e_multi_cluster_fail_cluster_connectivity
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_error()
