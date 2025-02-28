import json
from ipaddress import IPv4Address
from typing import Any, List

import kubernetes
from _pytest.fixtures import fixture
from kubetester import MongoDB, read_configmap
from kubetester.mongodb_multi import MultiClusterClient
from kubetester.operator import Operator
from tests.conftest import (
    LEGACY_CENTRAL_CLUSTER_NAME,
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

    # Members and MemberConfig fields should be empty in overrides for MultiCluster
    for idx, _ in enumerate(resource["spec"].get("shardOverrides", [])):
        if "members" in resource["spec"]["shards"][idx]:
            resource["spec"]["shardOverrides"][idx]["members"] = 0
        if "memberConfig" in resource["spec"]["shards"][idx]:
            resource["spec"]["shardOverrides"][idx]["memberConfig"] = None

    setup_cluster_spec_list(resource, "shard", shard_members_array or [1, 1, 1])
    setup_cluster_spec_list(resource, "configSrv", configsrv_members_array or [1, 1, 1])
    setup_cluster_spec_list(resource, "mongos", mongos_members_array or [1, 1, 1])

    resource.api = kubernetes.client.CustomObjectsApi(api_client=get_central_cluster_client())


class ClusterInfo:
    def __init__(self, cluster_name: str, cidr: IPv4Address, external_domain: str):
        self.cluster_name = cluster_name
        self.cidr = cidr
        self.external_domain = external_domain


KIND_SINGLE_CLUSTER = ClusterInfo("kind-kind", IPv4Address("172.18.255.200"), "kind-kind.interconnected")
KIND_E2E_CLUSTER_1 = ClusterInfo(
    "kind-e2e-cluster-1", IPv4Address("172.18.255.210"), "kind-e2e-cluster-1.interconnected"
)
KIND_E2E_CLUSTER_2 = ClusterInfo(
    "kind-e2e-cluster-2", IPv4Address("172.18.255.220"), "kind-e2e-cluster-2.interconnected"
)
KIND_E2E_CLUSTER_3 = ClusterInfo(
    "kind-e2e-cluster-3", IPv4Address("172.18.255.230"), "kind-e2e-cluster-3.interconnected"
)
KIND_E2E_OPERATOR = ClusterInfo("kind-e2e-operator", IPv4Address("172.18.255.200"), "kind-e2e-operator.interconnected")

cluster_map = {
    KIND_E2E_CLUSTER_1.cluster_name: KIND_E2E_CLUSTER_1,
    KIND_E2E_CLUSTER_2.cluster_name: KIND_E2E_CLUSTER_2,
    KIND_E2E_CLUSTER_3.cluster_name: KIND_E2E_CLUSTER_3,
    KIND_E2E_OPERATOR.cluster_name: KIND_E2E_OPERATOR,
    LEGACY_CENTRAL_CLUSTER_NAME: KIND_SINGLE_CLUSTER,
}


def get_cluster_info(cluster_name: str) -> ClusterInfo:
    val = cluster_map[cluster_name]
    if val is None:
        raise Exception(f"The {cluster_name} is not defined")
    return val


def _setup_external_access(
    resource: MongoDB, cluster_spec_type: str, cluster_member_list: List[str], enable_external_domain=True
):
    ports = [
        {
            "name": "mongodb",
            "port": 27017,
        },
    ]
    if cluster_spec_type in ["shard", ""]:
        ports = [
            {
                "name": "mongodb",
                "port": 27017,
            },
            {
                "name": "backup",
                "port": 27018,
            },
            {
                "name": "testing0",
                "port": 27019,
            },
        ]

    if "topology" in resource["spec"] and resource["spec"]["topology"] == "MultiCluster":
        resource["spec"]["externalAccess"] = {}
        for index, cluster_member_name in enumerate(cluster_member_list):
            resource["spec"][cluster_spec_type]["clusterSpecList"][index]["externalAccess"] = {
                "externalService": {
                    "spec": {
                        "type": "LoadBalancer",
                        "ports": ports,
                    }
                },
            }
            if enable_external_domain:
                resource["spec"][cluster_spec_type]["clusterSpecList"][index]["externalAccess"]["externalDomain"] = (
                    get_cluster_info(cluster_member_name).external_domain
                )
    else:
        resource["spec"]["externalAccess"] = {}
        if enable_external_domain:
            resource["spec"]["externalAccess"]["externalDomain"] = get_cluster_info(
                cluster_member_list[0]
            ).external_domain


def setup_external_access(resource: MongoDB, enable_external_domain=True):
    if "topology" in resource["spec"] and resource["spec"]["topology"] == "MultiCluster":
        _setup_external_access(
            resource=resource,
            cluster_spec_type="mongos",
            cluster_member_list=get_member_cluster_names(),
            enable_external_domain=enable_external_domain,
        )
        _setup_external_access(
            resource=resource,
            cluster_spec_type="configSrv",
            cluster_member_list=get_member_cluster_names(),
            enable_external_domain=enable_external_domain,
        )
        _setup_external_access(
            resource=resource,
            cluster_spec_type="shard",
            cluster_member_list=get_member_cluster_names(),
            enable_external_domain=enable_external_domain,
        )
    else:
        _setup_external_access(
            resource=resource,
            cluster_spec_type="",
            cluster_member_list=[get_central_cluster_name()],
            enable_external_domain=enable_external_domain,
        )


def get_dns_hosts_for_external_access(resource: MongoDB, cluster_member_list: List[str]) -> List[str]:
    hosts = []
    if "topology" in resource["spec"] and resource["spec"]["topology"] == "MultiCluster":
        for cluster_index, cluster_member_name in enumerate(cluster_member_list):
            cluster_info = get_cluster_info(cluster_member_name)
            ip = cluster_info.cidr
            # We skip the first IP as Istio Gateway takes it.
            ip_iterator = 1
            for i in range(resource["spec"]["mongos"]["clusterSpecList"][cluster_index]["members"]):
                fqdn = f"{resource.name}-mongos-{cluster_index}-{i}.{cluster_info.external_domain}"
                ip_for_fqdn = str(ip + ip_iterator)
                ip_iterator = ip_iterator + 1
                hosts.append((ip_for_fqdn, fqdn))
            for i in range(resource["spec"]["configSrv"]["clusterSpecList"][cluster_index]["members"]):
                fqdn = f"{resource.name}-config-{cluster_index}-{i}.{cluster_info.external_domain}"
                ip_for_fqdn = str(ip + ip_iterator)
                ip_iterator = ip_iterator + 1
                hosts.append((ip_for_fqdn, fqdn))
            for i in range(resource["spec"]["shard"]["clusterSpecList"][cluster_index]["members"]):
                fqdn = f"{resource.name}-0-{cluster_index}-{i}.{cluster_info.external_domain}"
                ip_for_fqdn = str(ip + ip_iterator)
                ip_iterator = ip_iterator + 1
                hosts.append((ip_for_fqdn, fqdn))
    else:
        cluster_info = get_cluster_info(cluster_member_list[0])
        ip = cluster_info.cidr
        ip_iterator = 0
        for i in range(resource["spec"]["mongosCount"]):
            fqdn = f"{resource.name}-mongos-{i}.{cluster_info.external_domain}"
            ip_for_fqdn = str(ip + ip_iterator)
            ip_iterator = ip_iterator + 1
            hosts.append((ip_for_fqdn, fqdn))
    return hosts


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


def get_member_cluster_client_using_cluster_mapping(
    resource_name: str, namespace: str, cluster_name: str
) -> MultiClusterClient:
    cluster_mapping = read_deployment_state(resource_name, namespace)["clusterMapping"]
    for m in get_member_cluster_clients(cluster_mapping):
        if m.cluster_name == cluster_name:
            return m
    raise Exception(f"cluster {cluster_name} not found in deployment state mapping {cluster_mapping}")


def get_mongos_service_names(resource: MongoDB):
    service_names = []
    for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(resource.name, resource.namespace):
        for member_idx in range(resource.mongos_members_in_cluster(cluster_member_client.cluster_name)):
            service_name = resource.mongos_service_name(member_idx, cluster_member_client.cluster_index)
            service_names.append(service_name)

    return service_names


def get_all_sharded_cluster_pod_names(resource: MongoDB):
    return get_mongos_pod_names(resource) + get_config_server_pod_names(resource) + get_all_shards_pod_names(resource)


def get_mongos_pod_names(resource: MongoDB):
    pod_names = []
    for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(resource.name, resource.namespace):
        for member_idx in range(resource.mongos_members_in_cluster(cluster_member_client.cluster_name)):
            pod_name = resource.mongos_pod_name(member_idx, cluster_member_client.cluster_index)
            pod_names.append(pod_name)

    return pod_names


def get_config_server_pod_names(resource: MongoDB):
    pod_names = []
    for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(resource.name, resource.namespace):
        for member_idx in range(resource.config_srv_members_in_cluster(cluster_member_client.cluster_name)):
            pod_name = resource.config_srv_pod_name(member_idx, cluster_member_client.cluster_index)
            pod_names.append(pod_name)

    return pod_names


def get_all_shards_pod_names(resource: MongoDB):
    pod_names = []
    for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(resource.name, resource.namespace):
        for shard_idx in range(resource["spec"]["shardCount"]):
            for member_idx in range(resource.shard_members_in_cluster(cluster_member_client.cluster_name)):
                pod_name = resource.shard_pod_name(shard_idx, member_idx, cluster_member_client.cluster_index)
                pod_names.append(pod_name)

    return pod_names


def read_deployment_state(resource_name: str, namespace: str) -> dict[str, Any]:
    deployment_state_cm = read_configmap(
        namespace,
        f"{resource_name}-state",
        get_central_cluster_client(),
    )
    state = json.loads(deployment_state_cm["state"])
    return state
