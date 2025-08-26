from kubetester import try_load
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.shardedcluster.conftest import (
    enable_multi_cluster_deployment,
    get_member_cluster_clients_using_cluster_mapping,
)

SHARD_COUNT = 2
RESIZED_STORAGE_SIZE = "2Gi"


# Note: This test can only be run in a cluster which uses - by default - a storageClass that is resizable.
# In Kind cluster you need to ensure that the resizable CSI driver has been installed. It should be automatically
# installed for new clusters


@fixture(scope="function")
def sc(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("sharded-cluster-pv-resize.yaml"),
        namespace=namespace,
    )

    if try_load(resource):
        return resource

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    return resource.update()


@mark.e2e_sharded_cluster_pv_resize
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_sharded_cluster_pv_resize
def test_create_sharded_cluster(sc: MongoDB):
    sc.assert_reaches_phase(Phase.Running, timeout=1800)


@mark.e2e_sharded_cluster_pv_resize
def test_sharded_cluster_resize_pvc_state_changes(sc: MongoDB):
    # Mongos do not support persistent storage
    sc["spec"]["shardPodSpec"]["persistence"]["multiple"]["journal"]["storage"] = RESIZED_STORAGE_SIZE
    sc["spec"]["shardPodSpec"]["persistence"]["multiple"]["data"]["storage"] = RESIZED_STORAGE_SIZE
    sc["spec"]["configSrvPodSpec"]["persistence"]["multiple"]["data"]["storage"] = RESIZED_STORAGE_SIZE
    sc["spec"]["configSrvPodSpec"]["persistence"]["multiple"]["journal"]["storage"] = RESIZED_STORAGE_SIZE

    sc.update()

    sc.assert_reaches_phase(Phase.Pending, timeout=800)
    sc.assert_reaches_phase(Phase.Running, timeout=3000)


@mark.e2e_sharded_cluster_pv_resize
def test_sharded_cluster_resize_finished(sc: MongoDB, namespace: str):
    for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
        cluster_idx = cluster_member_client.cluster_index
        shards_sts = []
        for shard_idx in range(SHARD_COUNT):
            shard_sts_name = sc.shard_statefulset_name(shard_idx, cluster_idx)
            shard_sts = cluster_member_client.read_namespaced_stateful_set(shard_sts_name, sc.namespace)
            shards_sts.append(shard_sts)

        config_sts_name = sc.config_srv_statefulset_name(cluster_idx)
        config_sts = cluster_member_client.read_namespaced_stateful_set(config_sts_name, sc.namespace)

        for sts in (config_sts, *shards_sts):
            assert sts.spec.volume_claim_templates[0].spec.resources.requests["storage"] == RESIZED_STORAGE_SIZE
            pvc_name = f"data-{sts.metadata.name}-0"
            pvc_data = cluster_member_client.read_namespaced_persistent_volume_claim(pvc_name, namespace)
            assert pvc_data.status.capacity["storage"] == RESIZED_STORAGE_SIZE

            pvc_name = f"journal-{sts.metadata.name}-0"
            pvc_data = cluster_member_client.read_namespaced_persistent_volume_claim(pvc_name, namespace)
            assert pvc_data.status.capacity["storage"] == RESIZED_STORAGE_SIZE

            initial_storage_size = "1Gi"
            pvc_name = f"logs-{sts.metadata.name}-0"
            pvc_data = cluster_member_client.read_namespaced_persistent_volume_claim(pvc_name, namespace)
            assert pvc_data.status.capacity["storage"] == initial_storage_size
