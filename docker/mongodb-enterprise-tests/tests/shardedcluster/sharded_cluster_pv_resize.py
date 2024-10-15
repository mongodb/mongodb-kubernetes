from kubernetes import client
from kubetester import MongoDB, get_statefulset, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.operator import Operator
from pytest import fixture, mark

SHARDED_CLUSTER_NAME = "sharded-resize"
RESIZED_STORAGE_SIZE = "2Gi"

# Note: This test can only be run in a cluster which uses - by default - a storageClass that is resizable; e.g., GKE
# For kind to work, you need to ensure that the resizable CSI driver has been installed, it will be installed
# Once you re-create your clusters


@fixture(scope="module")
def sharded_cluster(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("sharded-cluster-pv-resize.yaml"),
        namespace=namespace,
        name=SHARDED_CLUSTER_NAME,
    )
    resource.set_version(custom_mdb_version)
    try_load(resource)
    return resource


@mark.e2e_sharded_cluster_pv_resize
def test_install_operator(default_operator: Operator):
    default_operator.assert_is_running()


@mark.e2e_sharded_cluster_pv_resize
def test_create_sharded_cluster(sharded_cluster: MongoDB):
    sharded_cluster.update()
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1800)


@mark.e2e_sharded_cluster_pv_resize
def test_sharded_cluster_resize_pvc_state_changes(sharded_cluster: MongoDB):
    # Mongos do not support persistent storage
    sharded_cluster.load()
    sharded_cluster["spec"]["shardPodSpec"]["persistence"]["multiple"]["journal"]["storage"] = RESIZED_STORAGE_SIZE
    sharded_cluster["spec"]["shardPodSpec"]["persistence"]["multiple"]["data"]["storage"] = RESIZED_STORAGE_SIZE
    sharded_cluster["spec"]["configSrvPodSpec"]["persistence"]["multiple"]["data"]["storage"] = RESIZED_STORAGE_SIZE
    sharded_cluster["spec"]["configSrvPodSpec"]["persistence"]["multiple"]["journal"]["storage"] = RESIZED_STORAGE_SIZE
    sharded_cluster.update()
    sharded_cluster.assert_reaches_phase(Phase.Pending, timeout=400)
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=2000)


@mark.e2e_sharded_cluster_pv_resize
def test_sharded_cluster_resize_finished(sharded_cluster: MongoDB, namespace: str):
    shards = sharded_cluster["spec"]["shardCount"]
    sts_shards = []
    for i in range(shards):
        sts_shards.append(get_statefulset(namespace, f"{SHARDED_CLUSTER_NAME}-{i}"))

    sts_config = get_statefulset(namespace, f"{SHARDED_CLUSTER_NAME}-config")
    for sts in (sts_config, *sts_shards):
        assert sts.spec.volume_claim_templates[0].spec.resources.requests["storage"] == RESIZED_STORAGE_SIZE
        pvc_name = f"data-{sts.metadata.name}-0"
        pvc_data = client.CoreV1Api().read_namespaced_persistent_volume_claim(pvc_name, namespace)
        assert pvc_data.status.capacity["storage"] == RESIZED_STORAGE_SIZE

        pvc_name = f"journal-{sts.metadata.name}-0"
        pvc_data = client.CoreV1Api().read_namespaced_persistent_volume_claim(pvc_name, namespace)
        assert pvc_data.status.capacity["storage"] == RESIZED_STORAGE_SIZE

        initial_storage_size = "1Gi"
        pvc_name = f"logs-{sts.metadata.name}-0"
        pvc_data = client.CoreV1Api().read_namespaced_persistent_volume_claim(pvc_name, namespace)
        assert pvc_data.status.capacity["storage"] == initial_storage_size
