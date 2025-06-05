import logging

import kubernetes
from kubetester import try_load
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.conftest import (
    get_central_cluster_name,
    is_multi_cluster,
    update_coredns_hosts,
)
from tests.shardedcluster.conftest import (
    get_dns_hosts_for_external_access,
    setup_external_access,
)

SCALED_SHARD_COUNT = 2
logger = test_logger.get_test_logger(__name__)


@fixture(scope="function")
def sc(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("sharded-cluster.yaml"), namespace=namespace)

    if try_load(resource):
        return resource

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()
    setup_external_access(resource)

    if is_multi_cluster():
        raise Exception("This test has been designed to run only in Single Cluster mode")

    return resource.update()


# Even though this is theoretically not needed, it is useful for testing with Multi Cluster EVG hosts.
@mark.e2e_sharded_cluster_external_access
def test_disable_istio(disable_istio):
    logging.info("Istio disabled")


@mark.e2e_sharded_cluster_external_access
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_sharded_cluster_external_access
def test_update_coredns(cluster_clients: dict[str, kubernetes.client.ApiClient], sc: MongoDB):
    hosts = get_dns_hosts_for_external_access(resource=sc, cluster_member_list=[get_central_cluster_name()])
    for cluster_name, cluster_api in cluster_clients.items():
        update_coredns_hosts(hosts, cluster_name, api_client=cluster_api)


@mark.e2e_sharded_cluster_external_access
class TestShardedClusterCreation:

    def test_create_sharded_cluster(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running, timeout=1200)

    # Testing connectivity with External Access requires using the same DNS as deployed in Kube within
    # test_update_coredns. There's no easy way to set it up locally.
    @skip_if_local()
    def test_shards_were_configured_and_accessible(self, sc: MongoDB):
        tester = sc.tester()
        tester.assert_connectivity()
