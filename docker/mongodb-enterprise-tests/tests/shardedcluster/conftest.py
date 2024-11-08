import json
from typing import Any, List

import kubernetes
from _pytest.fixtures import fixture
from kubetester import MongoDB, read_configmap
from kubetester.mongodb_multi import MultiClusterClient
from kubetester.operator import Operator
from tests.conftest import (
    get_central_cluster_client,
    get_central_cluster_name,
    get_default_operator,
    get_member_cluster_clients,
    get_member_cluster_names,
    get_multi_cluster_operator,
    get_multi_cluster_operator_installation_config,
    get_operator_installation_config,
    is_multi_cluster,
)
from tests.multicluster.conftest import cluster_spec_list


@fixture(scope="module")
def operator(namespace: str) -> Operator:
    if is_multi_cluster():
        return get_multi_cluster_operator(
            namespace,
            get_central_cluster_name(),
            get_multi_cluster_operator_installation_config(namespace),
            get_central_cluster_client(),
            get_member_cluster_clients(),
            get_member_cluster_names(),
        )
    else:
        return get_default_operator(namespace, get_operator_installation_config(namespace))


def enable_multi_cluster_deployment(
    resource: MongoDB,
    shard_members_array: list[int] = None,
    mongos_members_array: list[int] = None,
    configsrv_members_array: list[int] = None,
):
    resource["spec"]["topology"] = "MultiCluster"
    resource["spec"]["mongodsPerShardCount"] = None
    resource["spec"]["mongosCount"] = None
    resource["spec"]["configServerCount"] = None

    setup_cluster_spec_list(resource, "shard", shard_members_array or [1, 1, 1])
    setup_cluster_spec_list(resource, "configSrv", configsrv_members_array or [1, 1, 1])
    setup_cluster_spec_list(resource, "mongos", mongos_members_array or [1, 1, 1])

    resource.api = kubernetes.client.CustomObjectsApi(api_client=get_central_cluster_client())


def setup_cluster_spec_list(resource: MongoDB, cluster_spec_type: str, members_array: list[int]):
    if cluster_spec_type not in resource["spec"]:
        resource["spec"][cluster_spec_type] = {}

    if "clusterSpecList" not in resource["spec"][cluster_spec_type]:
        resource["spec"][cluster_spec_type]["clusterSpecList"] = cluster_spec_list(
            get_member_cluster_names(), members_array
        )


def get_member_cluster_clients_using_cluster_mapping(resource_name: str, namespace: str) -> List[MultiClusterClient]:
    cluster_mapping = read_deployment_state(resource_name, namespace)["clusterMapping"]
    return get_member_cluster_clients(cluster_mapping)


def get_mongos_service_names(sc) -> [str]:
    service_names = []
    for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
        for member_idx in range(sc.mongos_members_in_cluster(cluster_member_client.cluster_name)):
            service_names.append(sc.mongos_service_name(member_idx, cluster_member_client.cluster_index))

    return service_names


def read_deployment_state(resource_name: str, namespace: str) -> dict[str, Any]:
    deployment_state_cm = read_configmap(
        namespace,
        f"{resource_name}-state",
        get_central_cluster_client(),
    )
    state = json.loads(deployment_state_cm["state"])
    return state
