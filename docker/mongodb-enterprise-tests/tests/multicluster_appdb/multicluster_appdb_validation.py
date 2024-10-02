import kubernetes
import kubernetes.client
import pytest
from kubernetes.client.rest import ApiException
from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark
from tests.multicluster.conftest import cluster_spec_list


@fixture(scope="module")
def appdb_member_cluster_names() -> list[str]:
    return ["kind-e2e-cluster-2", "kind-e2e-cluster-3"]


@fixture(scope="module")
def ops_manager(
    namespace: str,
    custom_appdb_version: str,
    custom_version: str,
    central_cluster_client: kubernetes.client.ApiClient,
    appdb_member_cluster_names: list[str],
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("multicluster_appdb_om.yaml"), namespace=namespace
    )
    try_load(resource)

    resource["spec"]["version"] = custom_version
    resource.create_admin_secret(api_client=central_cluster_client)

    resource["spec"]["applicationDatabase"] = {
        "topology": "MultiCluster",
        "clusterSpecList": cluster_spec_list(appdb_member_cluster_names, [1, 2]),
        "version": custom_appdb_version,
        "agent": {"logLevel": "DEBUG"},
    }

    return resource


@mark.e2e_multi_cluster_appdb_validation
def test_wait_for_webhook(namespace: str, multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_webhook()


@mark.usefixtures("multi_cluster_operator")
@mark.e2e_multi_cluster_appdb_validation
def test_validate_unique_cluster_name(
    ops_manager: MongoDBOpsManager, central_cluster_client: kubernetes.client.ApiClient
):
    ops_manager.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    ops_manager["spec"]["applicationDatabase"]["clusterSpecList"].append(
        {"clusterName": "kind-e2e-cluster-2", "members": 1}
    )

    with pytest.raises(
        ApiException,
        match=r"Multiple clusters with the same name \(kind-e2e-cluster-2\) are not allowed",
    ):
        ops_manager.update()


@mark.e2e_multi_cluster_appdb_validation
def test_non_empty_clusterspec_list(
    ops_manager: MongoDBOpsManager, central_cluster_client: kubernetes.client.ApiClient
):
    ops_manager.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = []

    with pytest.raises(
        ApiException,
        match=r"ClusterSpecList empty is not allowed\, please define at least one cluster",
    ):
        ops_manager.update()


@mark.e2e_multi_cluster_appdb_validation
def test_member_clusters_is_a_subset_of_kubeconfig(
    ops_manager: MongoDBOpsManager, central_cluster_client: kubernetes.client.ApiClient
):
    ops_manager.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    ops_manager["spec"]["applicationDatabase"]["clusterSpecList"].append(
        {"clusterName": "kind-e2e-cluster-4", "members": 1}
    )

    with pytest.raises(
        ApiException,
        match=r"The following clusters specified in ClusterSpecList is not present in Kubeconfig: \[kind-e2e-cluster-4\]",
    ):
        ops_manager.update()


@mark.e2e_multi_cluster_appdb_validation
def test_empty_cluster_spec_list_single_cluster(
    ops_manager: MongoDBOpsManager, central_cluster_client: kubernetes.client.ApiClient
):
    ops_manager.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    ops_manager["spec"]["applicationDatabase"]["topology"] = "SingleCluster"

    with pytest.raises(
        ApiException,
        match=r"Single cluster AppDB deployment should have empty clusterSpecList",
    ):
        ops_manager.update()
