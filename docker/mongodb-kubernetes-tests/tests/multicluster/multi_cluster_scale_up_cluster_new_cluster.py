from typing import Callable, List

import kubernetes
import pytest
from kubernetes import client
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongotester import with_tls
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.conftest import MULTI_CLUSTER_OPERATOR_NAME, run_kube_config_creation_tool
from tests.multicluster.conftest import cluster_spec_list

RESOURCE_NAME = "multi-replica-set"
BUNDLE_SECRET_NAME = f"prefix-{RESOURCE_NAME}-cert"


@pytest.fixture(scope="module")
def mongodb_multi_unmarshalled(
    namespace: str,
    multi_cluster_issuer_ca_configmap: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), RESOURCE_NAME, namespace)
    resource.set_version(custom_mdb_version)
    # ensure certs are created for the members during scale up
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])

    resource["spec"]["security"] = {
        "certsSecretPrefix": "prefix",
        "tls": {
            "ca": multi_cluster_issuer_ca_configmap,
        },
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource


@pytest.fixture(scope="module")
def server_certs(
    multi_cluster_issuer: str,
    mongodb_multi_unmarshalled: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):
    return create_multi_cluster_mongodb_tls_certs(
        multi_cluster_issuer,
        BUNDLE_SECRET_NAME,
        member_cluster_clients,
        central_cluster_client,
        mongodb_multi_unmarshalled,
    )


@pytest.fixture(scope="module")
def mongodb_multi(mongodb_multi_unmarshalled: MongoDBMulti, server_certs: str) -> MongoDBMulti:
    mongodb_multi_unmarshalled["spec"]["clusterSpecList"].pop()
    return mongodb_multi_unmarshalled.create()


@pytest.mark.e2e_multi_cluster_scale_up_cluster_new_cluster
def test_deploy_operator(
    install_multi_cluster_operator_set_members_fn: Callable[[List[str]], Operator],
    member_cluster_names: List[str],
    namespace: str,
):
    run_kube_config_creation_tool(member_cluster_names[:-1], namespace, namespace, member_cluster_names)
    # deploy the operator without the final cluster
    operator = install_multi_cluster_operator_set_members_fn(member_cluster_names[:-1])
    operator.assert_is_running()


@pytest.mark.e2e_multi_cluster_scale_up_cluster_new_cluster
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


@pytest.mark.e2e_multi_cluster_scale_up_cluster_new_cluster
def test_statefulsets_have_been_created_correctly(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    clients = {c.cluster_name: c for c in member_cluster_clients}

    # read all statefulsets except the last one
    statefulsets = mongodb_multi.read_statefulsets(member_cluster_clients[:-1])
    cluster_one_client = clients["kind-e2e-cluster-1"]
    cluster_one_sts = statefulsets[cluster_one_client.cluster_name]
    assert cluster_one_sts.status.ready_replicas == 2

    cluster_two_client = clients["kind-e2e-cluster-2"]
    cluster_two_sts = statefulsets[cluster_two_client.cluster_name]
    assert cluster_two_sts.status.ready_replicas == 1


@pytest.mark.e2e_multi_cluster_scale_up_cluster_new_cluster
def test_ops_manager_has_been_updated_correctly_before_scaling(mongodb_multi: MongoDBMulti):
    ac = mongodb_multi.get_automation_config_tester()
    ac.assert_processes_size(3)


@pytest.mark.e2e_multi_cluster_scale_up_cluster_new_cluster
def test_delete_deployment(namespace: str, central_cluster_client: kubernetes.client.ApiClient):
    client.AppsV1Api(api_client=central_cluster_client).delete_namespaced_deployment(
        MULTI_CLUSTER_OPERATOR_NAME, namespace
    )


@pytest.mark.e2e_multi_cluster_scale_up_cluster_new_cluster
def test_re_deploy_operator(
    install_multi_cluster_operator_set_members_fn: Callable[[List[str]], Operator],
    member_cluster_names: List[str],
    namespace: str,
):
    run_kube_config_creation_tool(member_cluster_names, namespace, namespace, member_cluster_names)

    # deploy the operator without all clusters
    operator = install_multi_cluster_operator_set_members_fn(member_cluster_names)
    operator.assert_is_running()


@pytest.mark.e2e_multi_cluster_scale_up_cluster_new_cluster
def test_add_new_cluster_to_mongodb_multi_resource(
    mongodb_multi: MongoDBMulti, member_cluster_clients: List[MultiClusterClient]
):
    mongodb_multi.load()
    mongodb_multi["spec"]["clusterSpecList"].append(
        {"members": 2, "clusterName": member_cluster_clients[-1].cluster_name}
    )
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=800)


@pytest.mark.e2e_multi_cluster_scale_up_cluster_new_cluster
def test_statefulsets_have_been_created_correctly_after_cluster_addition(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    clients = {c.cluster_name: c for c in member_cluster_clients}
    # read all statefulsets except the last one
    statefulsets = mongodb_multi.read_statefulsets(member_cluster_clients)
    cluster_one_client = clients["kind-e2e-cluster-1"]
    cluster_one_sts = statefulsets[cluster_one_client.cluster_name]
    assert cluster_one_sts.status.ready_replicas == 2

    cluster_two_client = clients["kind-e2e-cluster-2"]
    cluster_two_sts = statefulsets[cluster_two_client.cluster_name]
    assert cluster_two_sts.status.ready_replicas == 1

    cluster_three_client = clients["kind-e2e-cluster-3"]
    cluster_three_sts = statefulsets[cluster_three_client.cluster_name]
    assert cluster_three_sts.status.ready_replicas == 2


@pytest.mark.e2e_multi_cluster_scale_up_cluster_new_cluster
def test_ops_manager_has_been_updated_correctly_after_scaling(mongodb_multi: MongoDBMulti):
    ac = mongodb_multi.get_automation_config_tester()
    ac.assert_processes_size(5)


@skip_if_local
@pytest.mark.e2e_multi_cluster_scale_up_cluster_new_cluster
def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti, ca_path: str):
    tester = mongodb_multi.tester()
    tester.assert_connectivity(opts=[with_tls(use_tls=True, ca_path=ca_path)])
