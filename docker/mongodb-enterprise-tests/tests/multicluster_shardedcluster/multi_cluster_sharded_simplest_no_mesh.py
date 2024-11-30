import logging

import kubernetes
from kubetester import find_fixture, try_load
from kubetester.kubetester import ensure_ent_version, skip_if_local
from kubetester.mongodb import MongoDB, Phase
from kubetester.operator import Operator
from pytest import fixture, mark
from tests.conftest import get_member_cluster_names, update_coredns_hosts
from tests.shardedcluster.conftest import (
    enable_multi_cluster_deployment,
    get_dns_hosts_for_external_access,
    setup_external_access,
)

MDB_RESOURCE_NAME = "sh"


@fixture(scope="module")
def sharded_cluster(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("sharded-cluster-multi-cluster.yaml"), namespace=namespace, name=MDB_RESOURCE_NAME
    )

    if try_load(resource):
        return resource

    resource.set_version(ensure_ent_version(custom_mdb_version))

    enable_multi_cluster_deployment(resource=resource)
    setup_external_access(resource=resource)

    resource.set_architecture_annotation()

    return resource


@mark.e2e_multi_cluster_sharded_simplest_no_mesh
def test_disable_istio(disable_istio):
    logging.info("Istio disabled")


@mark.e2e_multi_cluster_sharded_simplest_no_mesh
def test_update_coredns(cluster_clients: dict[str, kubernetes.client.ApiClient], sharded_cluster: MongoDB):
    hosts = get_dns_hosts_for_external_access(resource=sharded_cluster, cluster_member_list=get_member_cluster_names())
    for cluster_name, cluster_api in cluster_clients.items():
        update_coredns_hosts(hosts, cluster_name, api_client=cluster_api)


@mark.e2e_multi_cluster_sharded_simplest_no_mesh
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_sharded_simplest_no_mesh
def test_sharded_cluster(sharded_cluster: MongoDB):
    sharded_cluster.update()
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1800)


# Testing connectivity with External Access requires using the same DNS as deployed in Kube within
# test_update_coredns. There's no easy way to set it up locally.
@skip_if_local()
@mark.e2e_multi_cluster_sharded_simplest_no_mesh
def test_shards_were_configured_and_accessible(sharded_cluster: MongoDB):
    hosts = get_dns_hosts_for_external_access(resource=sharded_cluster, cluster_member_list=get_member_cluster_names())
    mongos_hostnames = [item[1] for item in hosts if "mongos" in item[1]]
    # It's not obvious, but under the covers using Services and External Domain will ensure the tester respects
    # the supplied hosts (and only them).
    tester = sharded_cluster.tester(service_names=mongos_hostnames)
    tester.assert_connectivity()
