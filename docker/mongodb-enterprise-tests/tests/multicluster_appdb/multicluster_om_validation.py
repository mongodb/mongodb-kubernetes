import kubernetes
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
    custom_version: str,
    custom_appdb_version: str,
    appdb_member_cluster_names: list[str],
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("multicluster_appdb_om.yaml"), namespace=namespace
    )
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource["spec"]["version"] = custom_version
    resource["spec"]["topology"] = "MultiCluster"
    resource["spec"]["clusterSpecList"] = cluster_spec_list(["kind-e2e-cluster-2", "kind-e2e-cluster-3"], [2, 2])

    resource.allow_mdb_rc_versions()
    resource.create_admin_secret(api_client=central_cluster_client)

    resource["spec"]["backup"] = {"enabled": False}
    resource["spec"]["applicationDatabase"] = {
        "topology": "MultiCluster",
        "clusterSpecList": cluster_spec_list(appdb_member_cluster_names, [2, 3]),
        "version": custom_appdb_version,
        "agent": {"logLevel": "DEBUG"},
    }

    return resource


@mark.e2e_multi_cluster_om_validation
def test_wait_for_webhook(namespace: str, multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_webhook()


@mark.usefixtures("multi_cluster_operator")
@mark.e2e_multi_cluster_om_validation
def test_topology_is_specified(ops_manager: MongoDBOpsManager):
    del ops_manager["spec"]["topology"]

    with pytest.raises(
        ApiException,
        match=r"Topology 'MultiCluster' must be specified.*",
    ):
        ops_manager.update()


@mark.usefixtures("multi_cluster_operator")
@mark.e2e_multi_cluster_om_validation
def test_validate_cluster_spec_list(ops_manager: MongoDBOpsManager):
    ops_manager["spec"]["topology"] = "MultiCluster"
    ops_manager["spec"]["clusterSpecList"] = []

    with pytest.raises(
        ApiException,
        match=r"At least one ClusterSpecList entry must be specified for MultiCluster mode OM",
    ):
        ops_manager.update()
