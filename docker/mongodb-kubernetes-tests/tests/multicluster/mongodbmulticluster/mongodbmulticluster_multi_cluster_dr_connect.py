from typing import Dict

import kubernetes
import pytest
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.operator import Operator

from ..shared import multi_cluster_dr_connect as testhelper

MDB_RESOURCE = "multi-replica-set"


# this test is intended to run locally, using telepresence. Make sure to configure the cluster_context to api-server mapping
# in the "cluster_host_mapping" fixture before running it. It is intented to be run locally with the command: make e2e-telepresence test=e2e_mongodbmulticluster_multi_cluster_dr local=true
@pytest.fixture(scope="module")
def mongodb_multi(central_cluster_client: kubernetes.client.ApiClient, namespace: str) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodbmulticluster-multi-dr.yaml"), MDB_RESOURCE, namespace)

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    # return resource.load()
    return resource.create()


@pytest.fixture(scope="module")
def mongodb_multi_collection(mongodb_multi: MongoDBMulti):
    collection = mongodb_multi.tester().client["testdb"]
    return collection["testcollection"]


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_dr
def test_create_kube_config_file(cluster_clients: Dict):
    testhelper.test_create_kube_config_file(cluster_clients)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_dr
def test_deploy_operator(multi_cluster_operator: Operator):
    testhelper.test_deploy_operator(multi_cluster_operator)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_dr
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    testhelper.test_create_mongodb_multi(mongodb_multi)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_dr
def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti):
    testhelper.test_replica_set_is_reachable(mongodb_multi)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_dr
@pytest.mark.flaky(reruns=100, reruns_delay=6)
def test_add_test_data(mongodb_multi_collection):
    testhelper.test_add_test_data(mongodb_multi_collection)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_dr
def test_delete_member_3_cluster():
    testhelper.test_delete_member_3_cluster()


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_dr
def test_replica_set_is_reachable_after_deletetion(mongodb_multi: MongoDBMulti):
    testhelper.test_replica_set_is_reachable_after_deletetion(mongodb_multi)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_dr
def test_add_test_data_after_deletion(mongodb_multi_collection, capsys):
    testhelper.test_add_test_data_after_deletion(mongodb_multi_collection, capsys)
