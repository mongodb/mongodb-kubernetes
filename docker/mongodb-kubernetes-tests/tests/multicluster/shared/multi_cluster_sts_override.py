from typing import List

from kubernetes import client
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase


def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


def test_create_mongodb_multi(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


def test_statefulset_overrides(mongodb_multi: MongoDBMulti | MongoDB, member_cluster_clients: List[MultiClusterClient]):
    statefulsets = mongodb_multi.read_statefulsets(member_cluster_clients)

    # assert sts.podspec override in cluster1
    cluster_one_client = member_cluster_clients[0]
    cluster_one_sts = statefulsets[cluster_one_client.cluster_name]
    assert_container_in_sts("sidecar1", cluster_one_sts)
    assert "multi-replica-set" in cluster_one_sts.spec.template.metadata.labels["app"]

    # assert sts.podspec override in cluster2
    cluster_two_client = member_cluster_clients[1]
    cluster_two_sts = statefulsets[cluster_two_client.cluster_name]
    assert_container_in_sts("sidecar2", cluster_two_sts)


def test_access_modes_pvc(
    mongodb_multi: MongoDBMulti | MongoDB,
    member_cluster_clients: List[MultiClusterClient],
    namespace: str,
):
    pvc = client.CoreV1Api(api_client=member_cluster_clients[0].api_client).read_namespaced_persistent_volume_claim(
        f"data-{mongodb_multi.name}-{0}-{0}", namespace
    )

    assert "ReadWriteOnce" in pvc.spec.access_modes


def assert_container_in_sts(container_name: str, sts: client.V1StatefulSet):
    container_names = [c.name for c in sts.spec.template.spec.containers]
    assert container_name in container_names
