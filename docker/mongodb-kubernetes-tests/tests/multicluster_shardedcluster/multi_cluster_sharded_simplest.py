from kubetester import find_fixture, try_load
from kubetester.kubetester import ensure_ent_version
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import get_member_cluster_clients, get_member_cluster_names
from tests.multicluster.conftest import cluster_spec_list

MDB_RESOURCE_NAME = "sh"


@fixture(scope="module")
def sharded_cluster(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("sharded-cluster-multi-cluster.yaml"), namespace=namespace, name=MDB_RESOURCE_NAME
    )

    try_load(resource)
    return resource


@mark.e2e_multi_cluster_sharded_simplest
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


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
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_multi_cluster_sharded_simplest
def test_statefulsets_multi_cluster_identity(namespace: str):
    """Regression test: sharded cluster StatefulSets in member clusters must carry no
    ownerReferences and must carry the MongoDBMultiResource annotation.

    No ownerReferences: a cross-cluster ownerReference points to the MongoDBShardedCluster
    CR that only exists in the central cluster. The Kubernetes GC treats the StatefulSet as
    an orphan and deletes it immediately, causing an infinite create-delete reconciliation loop.
    Cleanup on CR deletion is handled through explicit label-based deletion instead.

    MongoDBMultiResource annotation: replaces ownerReferences as the identifier that watch
    predicates and the OM connection factory use to map StatefulSets back to their parent CR."""
    for mcc in get_member_cluster_clients():
        sts_list = mcc.list_namespaced_stateful_sets(namespace)
        for sts in sts_list.items:
            owner_refs = sts.metadata.owner_references
            assert not owner_refs, (
                f"StatefulSet {sts.metadata.name} in cluster {mcc.cluster_name} must have no "
                f"ownerReferences in multi-cluster mode, but got: {owner_refs}"
            )
            annotation_value = (sts.metadata.annotations or {}).get("MongoDBMultiResource")
            assert annotation_value == MDB_RESOURCE_NAME, (
                f"StatefulSet {sts.metadata.name} in cluster {mcc.cluster_name} must carry "
                f"annotation 'MongoDBMultiResource={MDB_RESOURCE_NAME}', but got: {annotation_value!r}"
            )
