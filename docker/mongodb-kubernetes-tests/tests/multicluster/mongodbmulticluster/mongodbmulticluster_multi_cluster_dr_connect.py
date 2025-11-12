import subprocess
import time
from typing import Dict

import kubernetes
import pytest
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.operator import Operator
from kubetester.phase import Phase

TEST_DATA = {"_id": "unique_id", "name": "John", "address": "Highway 37", "age": 30}

CLUSTER_TO_DELETE = "member-3a"


# this test is intended to run locally, using telepresence. Make sure to configure the cluster_context to api-server mapping
# in the "cluster_host_mapping" fixture before running it. It is intented to be run locally with the command: make e2e-telepresence test=e2e_multi_cluster_dr local=true
@pytest.fixture(scope="module")
def mongodb_multi(central_cluster_client: kubernetes.client.ApiClient, namespace: str) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodbmulticluster-multi-dr.yaml"), "multi-replica-set", namespace)

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    # return resource.load()
    return resource.create()


@pytest.fixture(scope="module")
def mongodb_multi_collection(mongodb_multi: MongoDBMulti):
    collection = mongodb_multi.tester().client["testdb"]
    return collection["testcollection"]


@pytest.mark.e2e_multi_cluster_dr
def test_create_kube_config_file(cluster_clients: Dict):
    clients = cluster_clients
    assert len(clients) == 4


@pytest.mark.e2e_multi_cluster_dr
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@pytest.mark.e2e_multi_cluster_dr
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_multi_cluster_dr
def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti):
    tester = mongodb_multi.tester()
    tester.assert_connectivity()


@pytest.mark.e2e_multi_cluster_dr
@pytest.mark.flaky(reruns=100, reruns_delay=6)
def test_add_test_data(mongodb_multi_collection):
    mongodb_multi_collection.insert_one(TEST_DATA)


@pytest.mark.e2e_multi_cluster_dr
def test_delete_member_3_cluster():
    # delete 3rd cluster with gcloud command
    # gcloud container clusters delete member-3a --zone us-west1-a
    subprocess.call(
        [
            "gcloud",
            "container",
            "clusters",
            "delete",
            CLUSTER_TO_DELETE,
            "--zone",
            "us-west1-a",
            "--quiet",
        ],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )


@pytest.mark.e2e_multi_cluster_dr
def test_replica_set_is_reachable_after_deletetion(mongodb_multi: MongoDBMulti):
    tester = mongodb_multi.tester()
    tester.assert_connectivity()


@pytest.mark.e2e_multi_cluster_dr
def test_add_test_data_after_deletion(mongodb_multi_collection, capsys):
    max_attempts = 100
    while max_attempts > 0:
        try:
            mongodb_multi_collection.insert_one(TEST_DATA.copy())
            return
        except Exception as e:
            with capsys.disabled():
                print(e)
            max_attempts -= 1
            time.sleep(6)
