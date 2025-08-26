from collections import defaultdict
from typing import Dict, List

from kubernetes import client
from kubetester import find_fixture, try_load
from kubetester.kubetester import ensure_ent_version
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.conftest import get_member_cluster_api_client
from tests.shardedcluster.conftest import (
    enable_multi_cluster_deployment,
    setup_external_access,
)

MDB_RESOURCE_NAME = "sh"
logger = test_logger.get_test_logger(__name__)


@fixture(scope="module")
def sharded_cluster(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("sharded-cluster-multi-cluster.yaml"), namespace=namespace, name=MDB_RESOURCE_NAME
    )

    if try_load(resource):
        return resource

    resource.set_version(ensure_ent_version(custom_mdb_version))

    enable_multi_cluster_deployment(resource=resource)
    setup_external_access(resource=resource, enable_external_domain=False)

    resource.set_architecture_annotation()

    return resource


@mark.e2e_multi_cluster_sharded_external_access_no_ext_domain
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_sharded_external_access_no_ext_domain
def test_sharded_cluster(sharded_cluster: MongoDB):
    sharded_cluster.update()
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1400)


@mark.e2e_multi_cluster_sharded_external_access_no_ext_domain
def test_services_were_created(sharded_cluster: MongoDB, namespace: str):
    resource_name = sharded_cluster.name
    expected_services: Dict[str, List[str]] = defaultdict(list)

    # All components get a headless service and per-pod services
    # Config server also gets an additional headless service suffixed -cs
    config_clusters = sharded_cluster["spec"]["configSrv"]["clusterSpecList"]
    for idx, cluster_spec in enumerate(config_clusters):
        members = cluster_spec["members"]
        expected_services[cluster_spec["clusterName"]].append(f"{resource_name}-config-{idx}-svc")
        for pod in range(members):
            expected_services[cluster_spec["clusterName"]].append(f"{resource_name}-config-{idx}-{pod}-svc")

    # Mongos also get an external service per pod
    mongos_clusters = sharded_cluster["spec"]["mongos"]["clusterSpecList"]
    for idx, cluster_spec in enumerate(mongos_clusters):
        members = cluster_spec["members"]
        cluster_name = cluster_spec["clusterName"]
        expected_services[cluster_name].append(f"{resource_name}-mongos-{idx}-svc")
        for pod in range(members):
            expected_services[cluster_name].append(f"{resource_name}-mongos-{idx}-{pod}-svc")
            expected_services[cluster_name].append(f"{resource_name}-mongos-{idx}-{pod}-svc-external")

    shard_count = sharded_cluster["spec"]["shardCount"]
    shard_clusters = sharded_cluster["spec"]["shard"]["clusterSpecList"]
    for shard in range(shard_count):
        for idx, cluster_spec in enumerate(shard_clusters):
            members = cluster_spec["members"]
            cluster_name = cluster_spec["clusterName"]
            expected_services[cluster_name].append(f"{resource_name}-{shard}-{idx}-svc")
            for pod in range(members):
                expected_services[cluster_name].append(f"{resource_name}-{shard}-{idx}-{pod}-svc")

    logger.debug("Asserting the following services exist:")
    for cluster, services in expected_services.items():
        logger.debug(f"Cluster: {cluster}, service count: {len(services)}")
        logger.debug(f"Services: {services}")

    # Assert that each expected service exists in its corresponding cluster and no extra services exist
    for cluster, expected_services in expected_services.items():
        api_client = get_member_cluster_api_client(cluster)  # Retrieve the API client for the cluster
        svc_list = client.CoreV1Api(api_client=api_client).list_namespaced_service(namespace)
        actual_services = map(lambda x: x.metadata.name, svc_list.items)
        assert sorted(expected_services) == sorted(actual_services)
