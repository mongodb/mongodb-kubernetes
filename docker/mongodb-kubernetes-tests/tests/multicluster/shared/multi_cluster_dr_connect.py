import subprocess
import time
from typing import Dict

from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.operator import Operator
from kubetester.phase import Phase

TEST_DATA = {"_id": "unique_id", "name": "John", "address": "Highway 37", "age": 30}

CLUSTER_TO_DELETE = "member-3a"


def test_create_kube_config_file(cluster_clients: Dict):
    clients = cluster_clients
    assert len(clients) == 4


def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


def test_create_mongodb_multi(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=600)


def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti | MongoDB):
    tester = mongodb_multi.tester()
    tester.assert_connectivity()


def test_add_test_data(mongodb_multi_collection):
    mongodb_multi_collection.insert_one(TEST_DATA)


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


def test_replica_set_is_reachable_after_deletetion(mongodb_multi: MongoDBMulti | MongoDB):
    tester = mongodb_multi.tester()
    tester.assert_connectivity()


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
