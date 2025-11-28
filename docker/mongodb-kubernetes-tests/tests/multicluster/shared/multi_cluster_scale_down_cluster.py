from typing import List

import kubernetes
import pytest
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongotester import with_tls
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase


def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


def test_create_mongodb_multi(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


def test_statefulsets_have_been_created_correctly(
    mongodb_multi: MongoDBMulti | MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    statefulsets = mongodb_multi.read_statefulsets(member_cluster_clients)

    assert len(statefulsets) == 3

    cluster_one_client = member_cluster_clients[0]
    cluster_one_sts = statefulsets[cluster_one_client.cluster_name]
    assert cluster_one_sts.status.ready_replicas == 2

    cluster_two_client = member_cluster_clients[1]
    cluster_two_sts = statefulsets[cluster_two_client.cluster_name]
    assert cluster_two_sts.status.ready_replicas == 1

    cluster_three_client = member_cluster_clients[2]
    cluster_three_sts = statefulsets[cluster_three_client.cluster_name]
    assert cluster_three_sts.status.ready_replicas == 2


def test_ops_manager_has_been_updated_correctly_before_scaling():
    ac = AutomationConfigTester()
    ac.assert_processes_size(5)


def test_scale_mongodb_multi(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.load()
    # remove first and last cluster
    mongodb_multi["spec"]["clusterSpecList"] = [mongodb_multi["spec"]["clusterSpecList"][1]]
    mongodb_multi.update()

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1800, ignore_errors=True)


def test_statefulsets_have_been_scaled_down_correctly(
    mongodb_multi: MongoDBMulti | MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    statefulsets = mongodb_multi.read_statefulsets([member_cluster_clients[1]])

    with pytest.raises(kubernetes.client.exceptions.ApiException) as e:
        mongodb_multi.read_statefulsets([member_cluster_clients[0]])
        assert e.value.reason == "Not Found"

    # there should only be one statefulset in the second cluster
    cluster_two_client = member_cluster_clients[1]
    cluster_two_sts = statefulsets[cluster_two_client.cluster_name]
    assert cluster_two_sts.status.ready_replicas == 1

    # there should be no statefulsets in the last cluster
    with pytest.raises(kubernetes.client.exceptions.ApiException) as e:
        mongodb_multi.read_statefulsets([member_cluster_clients[2]])
        assert e.value.reason == "Not Found"


def test_ops_manager_has_been_updated_correctly_after_scaling():
    ac = AutomationConfigTester()
    ac.assert_processes_size(1)


def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti | MongoDB, ca_path: str):
    # there should only be one member in cluster 2 so there is just a single service.
    tester = mongodb_multi.tester(service_names=[f"{mongodb_multi.name}-1-0-svc"])
    tester.assert_connectivity(opts=[with_tls(use_tls=True, ca_path=ca_path)])
