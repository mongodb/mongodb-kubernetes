import pytest
import kubernetes
from typing import List


from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.operator import Operator
from kubetester import delete_statefulset


@pytest.fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient, namespace: str
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi.yaml"), "multi-replica-set", namespace
    )

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    return resource.create()


@pytest.mark.e2e_multi_cluster_member_cluster_watch
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@pytest.mark.e2e_multi_cluster_member_cluster_watch
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=400)


@pytest.mark.e2e_multi_cluster_member_cluster_watch
def test_delete_member_cluster_sts(
    namespace: str,
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    # delete the statefulset in cluster1
    stsname = "{}-0".format(mongodb_multi.name)
    delete_statefulset(
        namespace=namespace,
        name=stsname,
        api_client=member_cluster_clients[0].api_client,
    )

    # abadons running phase since the statefulset in cluster1 has been deleted
    mongodb_multi.assert_abandons_phase(Phase.Running)

    # the operator should reconcile and recreate the statefulset
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=400)
