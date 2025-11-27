from typing import Dict, List

import kubernetes
from kubeobject import CustomObject
from kubernetes import client
from kubetester import (
    create_or_update_configmap,
    create_or_update_secret,
    delete_cluster_role,
    delete_cluster_role_binding,
    delete_statefulset,
    read_secret,
    statefulset_is_deleted,
)
from kubetester.kubetester import KubernetesTester, run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.conftest import (
    run_multi_cluster_recovery_tool,
)
from tests.constants import MULTI_CLUSTER_OPERATOR_NAME
from tests.multicluster import prepare_multi_cluster_namespaces
from tests.multicluster.conftest import (
    create_service_entries_objects,
)

from .multi_cluster_clusterwide import create_namespace

FAILED_MEMBER_CLUSTER_NAME = "kind-e2e-cluster-3"


def test_label_operator_namespace(namespace: str, central_cluster_client: kubernetes.client.ApiClient):
    api = client.CoreV1Api(api_client=central_cluster_client)

    labels = {"istio-injection": "enabled"}
    ns = api.read_namespace(name=namespace)

    ns.metadata.labels.update(labels)
    api.replace_namespace(name=namespace, body=ns)


def test_create_namespaces(
    namespace: str,
    mdba_ns: str,
    mdbb_ns: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    evergreen_task_id: str,
    multi_cluster_operator_installation_config: Dict[str, str],
):
    image_pull_secret_name = multi_cluster_operator_installation_config["registry.imagePullSecrets"]
    image_pull_secret_data = read_secret(namespace, image_pull_secret_name)

    create_namespace(
        central_cluster_client,
        member_cluster_clients,
        evergreen_task_id,
        mdba_ns,
        image_pull_secret_name,
        image_pull_secret_data,
    )

    create_namespace(
        central_cluster_client,
        member_cluster_clients,
        evergreen_task_id,
        mdbb_ns,
        image_pull_secret_name,
        image_pull_secret_data,
    )


def test_create_service_entry(service_entries: List[CustomObject]):
    for service_entry in service_entries:
        service_entry.update()


def test_delete_cluster_role_and_binding(
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
):
    role_names = [
        "mongodb-kubernetes-operator-multi-cluster-role",
        "mongodb-kubernetes-operator-multi-cluster",
        "mongodb-kubernetes-operator-multi-cluster-role-binding",
    ]

    for name in role_names:
        delete_cluster_role(name, central_cluster_client)
        delete_cluster_role_binding(name, central_cluster_client)

    for name in role_names:
        for client in member_cluster_clients:
            delete_cluster_role(name, client.api_client)
            delete_cluster_role_binding(name, client.api_client)


def test_deploy_operator(install_operator: Operator):
    install_operator.assert_is_running()


def test_prepare_namespace(
    multi_cluster_operator_installation_config: Dict[str, str],
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_name: str,
    mdba_ns: str,
    mdbb_ns: str,
):
    prepare_multi_cluster_namespaces(
        mdba_ns,
        multi_cluster_operator_installation_config,
        member_cluster_clients,
        central_cluster_name,
    )

    prepare_multi_cluster_namespaces(
        mdbb_ns,
        multi_cluster_operator_installation_config,
        member_cluster_clients,
        central_cluster_name,
    )


def test_copy_configmap_and_secret_across_ns(
    namespace: str,
    central_cluster_client: client.ApiClient,
    mdba_ns: str,
    mdbb_ns: str,
):
    data = KubernetesTester.read_configmap(namespace, "my-project", api_client=central_cluster_client)
    data["projectName"] = mdba_ns
    create_or_update_configmap(mdba_ns, "my-project", data, api_client=central_cluster_client)

    data["projectName"] = mdbb_ns
    create_or_update_configmap(mdbb_ns, "my-project", data, api_client=central_cluster_client)

    data = read_secret(namespace, "my-credentials", api_client=central_cluster_client)
    create_or_update_secret(mdba_ns, "my-credentials", data, api_client=central_cluster_client)
    create_or_update_secret(mdbb_ns, "my-credentials", data, api_client=central_cluster_client)


def test_create_mongodb_multi_nsa_nsb(mongodb_multi_a: MongoDBMulti | MongoDB, mongodb_multi_b: MongoDBMulti | MongoDB):
    mongodb_multi_a.assert_reaches_phase(Phase.Running, timeout=1500)
    mongodb_multi_b.assert_reaches_phase(Phase.Running, timeout=1500)


def test_update_service_entry_block_failed_cluster_traffic(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
):
    # TODO: add a way to simulate local operator connection cut-off
    healthy_cluster_names = [
        cluster_name for cluster_name in member_cluster_names if cluster_name != FAILED_MEMBER_CLUSTER_NAME
    ]

    service_entries = create_service_entries_objects(namespace, central_cluster_client, healthy_cluster_names)
    for service_entry in service_entries:
        print(f"service_entry={service_entries}")
        service_entry.update()


def test_delete_database_statefulsets_in_failed_cluster(
    mongodb_multi_a: MongoDBMulti | MongoDB,
    mongodb_multi_b: MongoDBMulti | MongoDB,
    mdba_ns: str,
    mdbb_ns: str,
    member_cluster_names: list[str],
    member_cluster_clients: List[MultiClusterClient],
):
    failed_cluster_idx = member_cluster_names.index(FAILED_MEMBER_CLUSTER_NAME)
    sts_a_name = f"{mongodb_multi_a.name}-{failed_cluster_idx}"
    sts_b_name = f"{mongodb_multi_b.name}-{failed_cluster_idx}"

    try:
        delete_statefulset(
            mdba_ns,
            sts_a_name,
            propagation_policy="Background",
            api_client=member_cluster_clients[2].api_client,
        )
        delete_statefulset(
            mdbb_ns,
            sts_b_name,
            propagation_policy="Background",
            api_client=member_cluster_clients[2].api_client,
        )

    except kubernetes.client.ApiException as e:
        if e.status != 404:
            raise e

    run_periodically(
        lambda: statefulset_is_deleted(
            mdba_ns,
            sts_a_name,
            api_client=member_cluster_clients[failed_cluster_idx].api_client,
        ),
        timeout=120,
    )
    run_periodically(
        lambda: statefulset_is_deleted(
            mdbb_ns,
            sts_b_name,
            api_client=member_cluster_clients[failed_cluster_idx].api_client,
        ),
        timeout=120,
    )


def test_mongodb_multi_nsa_enters_failed_stated(mongodb_multi_a: MongoDBMulti | MongoDB):
    mongodb_multi_a.load()
    mongodb_multi_a.assert_reaches_phase(Phase.Failed, timeout=100)


def test_mongodb_multi_nsb_enters_failed_stated(mongodb_multi_b: MongoDBMulti | MongoDB):
    mongodb_multi_b.load()
    mongodb_multi_b.assert_reaches_phase(Phase.Failed, timeout=100)


def test_recover_operator_remove_cluster(
    member_cluster_names: List[str],
    namespace: str,
    mdba_ns: str,
    mdbb_ns: str,
    central_cluster_client: kubernetes.client.ApiClient,
):
    return_code = run_multi_cluster_recovery_tool(member_cluster_names[:-1], namespace, mdba_ns, True)
    assert return_code == 0
    operator = Operator(
        name=MULTI_CLUSTER_OPERATOR_NAME,
        namespace=namespace,
        api_client=central_cluster_client,
    )
    operator._wait_for_operator_ready()
    operator.assert_is_running()


def test_mongodb_multi_nsa_recovers_removing_cluster(mongodb_multi_a: MongoDBMulti | MongoDB):
    mongodb_multi_a.load()

    mongodb_multi_a["metadata"]["annotations"]["failedClusters"] = None
    mongodb_multi_a["spec"]["clusterSpecList"].pop()
    mongodb_multi_a.update()

    mongodb_multi_a.assert_reaches_phase(Phase.Running, timeout=1500)


def test_mongodb_multi_nsb_recovers_removing_cluster(mongodb_multi_b: MongoDBMulti | MongoDB):
    mongodb_multi_b.load()

    mongodb_multi_b["metadata"]["annotations"]["failedClusters"] = None
    mongodb_multi_b["spec"]["clusterSpecList"].pop()
    mongodb_multi_b.update()
    mongodb_multi_b.assert_reaches_phase(Phase.Running, timeout=1500)
