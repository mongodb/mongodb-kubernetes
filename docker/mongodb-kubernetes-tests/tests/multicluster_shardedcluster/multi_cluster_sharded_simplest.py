from kubetester import find_fixture, try_load
from kubetester.kubetester import ensure_ent_version
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import get_member_cluster_names
from tests.multicluster.conftest import cluster_spec_list

MDB_RESOURCE_NAME = "sh"


@fixture(scope="module")
def sharded_cluster(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("sharded-cluster-multi-cluster.yaml"), namespace=namespace, name=MDB_RESOURCE_NAME
    )

    if try_load(resource):
        return resource

    return resource


@mark.e2e_multi_cluster_sharded_simplest
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_sharded_simplest
def test_create(sharded_cluster: MongoDB, custom_mdb_version: str, issuer_ca_configmap: str):
    sharded_cluster.set_version(ensure_ent_version(custom_mdb_version))

    sharded_cluster["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [2, 2, 1])
    sharded_cluster["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [2, 2, 1])
    sharded_cluster["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 2, 1])
    sharded_cluster.set_architecture_annotation()
    sharded_cluster.update()


@mark.e2e_multi_cluster_sharded_simplest
def test_sharded_cluster(sharded_cluster: MongoDB):
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1400)
