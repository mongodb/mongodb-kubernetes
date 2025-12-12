from typing import List

from kubernetes import client
from kubetester import get_statefulset
from kubetester.mongodb_multi import MongoDB, MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase

RESOURCE_NAME = "multi-replica-set-pvc-resize"
RESIZED_STORAGE_SIZE = "2Gi"


def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


def test_create_mongodb_multi(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=2000)


def test_mongodb_multi_resize_pvc_state_changes(mongodb_multi: MongoDBMulti | MongoDB):
    # Update the resource
    mongodb_multi.load()
    mongodb_multi["spec"]["statefulSet"]["spec"]["volumeClaimTemplates"][0]["spec"]["resources"]["requests"][
        "storage"
    ] = RESIZED_STORAGE_SIZE
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Pending, timeout=400)
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


def test_mongodb_multi_resize_finished(
    mongodb_multi: MongoDBMulti | MongoDB, namespace: str, member_cluster_clients: List[MultiClusterClient]
):
    statefulsets = []
    for i, c in enumerate(member_cluster_clients):
        statefulsets.append((get_statefulset(namespace, f"{RESOURCE_NAME}-{i}", c.api_client), c.api_client))

    for sts, c in statefulsets:
        assert sts.spec.volume_claim_templates[0].spec.resources.requests["storage"] == RESIZED_STORAGE_SIZE
        first_pvc_name = f"data-{sts.metadata.name}-0"
        pvc = client.CoreV1Api(api_client=c).read_namespaced_persistent_volume_claim(first_pvc_name, namespace)
        assert pvc.status.capacity["storage"] == RESIZED_STORAGE_SIZE
