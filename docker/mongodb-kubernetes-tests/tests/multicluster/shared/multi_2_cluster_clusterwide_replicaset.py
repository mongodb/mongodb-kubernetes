from typing import Dict, List

import kubernetes
from kubetester import (
    create_or_update_configmap,
    create_or_update_secret,
    read_configmap,
    read_secret,
)
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.multicluster import prepare_multi_cluster_namespaces
from tests.multicluster.conftest import create_namespace


def test_create_kube_config_file(cluster_clients: Dict, member_cluster_names: List[str]):
    clients = cluster_clients

    assert len(clients) == 2
    assert member_cluster_names[0] in clients
    assert member_cluster_names[1] in clients


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
    image_pull_secret_data = read_secret(namespace, image_pull_secret_name, api_client=central_cluster_client)

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


def test_deploy_operator(multi_cluster_operator_clustermode: Operator):
    multi_cluster_operator_clustermode.assert_is_running()


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
        skip_central_cluster=False,
    )

    prepare_multi_cluster_namespaces(
        mdbb_ns,
        multi_cluster_operator_installation_config,
        member_cluster_clients,
        central_cluster_name,
        skip_central_cluster=False,
    )


def test_copy_configmap_and_secret_across_ns(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    multi_cluster_operator_installation_config: Dict[str, str],
    mdba_ns: str,
    mdbb_ns: str,
):
    data = read_configmap(namespace, "my-project", api_client=central_cluster_client)
    data["projectName"] = mdba_ns
    create_or_update_configmap(mdba_ns, "my-project", data, api_client=central_cluster_client)

    data["projectName"] = mdbb_ns
    create_or_update_configmap(mdbb_ns, "my-project", data, api_client=central_cluster_client)

    data = read_secret(namespace, "my-credentials", api_client=central_cluster_client)
    create_or_update_secret(mdba_ns, "my-credentials", data, api_client=central_cluster_client)
    create_or_update_secret(mdbb_ns, "my-credentials", data, api_client=central_cluster_client)


def test_create_mongodb_multi_nsa(mongodb_multi_a: MongoDBMulti | MongoDB):
    mongodb_multi_a.assert_reaches_phase(Phase.Running, timeout=800)


def test_enable_mongodb_multi_nsa_auth(mongodb_multi_a: MongoDBMulti | MongoDB):
    mongodb_multi_a.reload()
    mongodb_multi_a["spec"]["authentication"] = (
        {
            "agents": {"mode": "SCRAM"},
            "enabled": True,
            "modes": ["SCRAM"],
        },
    )


def test_create_mongodb_multi_nsb(mongodb_multi_b: MongoDBMulti | MongoDB):
    mongodb_multi_b.assert_reaches_phase(Phase.Running, timeout=800)
