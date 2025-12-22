from typing import List

import kubetester
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
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=600)


def test_statefulsets_have_been_created_correctly(
    mongodb_multi: MongoDBMulti | MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    # Even though we already verified, in previous test, that the MongoDBMultiCluster resource's phase is running (that would mean all STSs are ready);
    # checking the expected number of replicas for STS makes the test flaky because of an issue mentioned in detail in this ticket https://jira.mongodb.org/browse/CLOUDP-329231.
    # That's why we are waiting for STS to have expected number of replicas. This change can be reverted when we make the proper fix as
    # mentioned in the above ticket.
    def fn():
        cluster_one_client = member_cluster_clients[0]
        cluster_one_statefulsets = mongodb_multi.read_statefulsets([cluster_one_client])
        return cluster_one_statefulsets[cluster_one_client.cluster_name].status.ready_replicas == 1

    kubetester.wait_until(fn, timeout=60, message="Verifying sts has correct number of replicas in cluster one")

    def fn():
        cluster_two_client = member_cluster_clients[1]
        cluster_two_statefulsets = mongodb_multi.read_statefulsets([cluster_two_client])
        return cluster_two_statefulsets[cluster_two_client.cluster_name].status.ready_replicas == 1

    kubetester.wait_until(fn, timeout=60, message="Verifying sts has correct number of replicas in cluster two")

    def fn():
        cluster_three_client = member_cluster_clients[2]
        cluster_three_statefulsets = mongodb_multi.read_statefulsets([cluster_three_client])
        return cluster_three_statefulsets[cluster_three_client.cluster_name].status.ready_replicas == 1

    kubetester.wait_until(fn, timeout=60, message="Verifying sts has correct number of replicas in cluster three")


def test_ops_manager_has_been_updated_correctly_before_scaling():
    ac = AutomationConfigTester()
    ac.assert_processes_size(3)


def test_scale_mongodb_multi(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.load()
    mongodb_multi["spec"]["clusterSpecList"][0]["members"] = 2
    mongodb_multi["spec"]["clusterSpecList"][1]["members"] = 1
    mongodb_multi["spec"]["clusterSpecList"][2]["members"] = 2
    mongodb_multi.update()

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1800)


def test_statefulsets_have_been_scaled_up_correctly(
    mongodb_multi: MongoDBMulti | MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    # Even though we already verified, in previous test, that the MongoDBMultiCluster resource's phase is running (that would mean all STSs are ready);
    # checking the expected number of replicas for STS makes the test flaky because of an issue mentioned in detail in this ticket https://jira.mongodb.org/browse/CLOUDP-329231.
    # That's why we are waiting for STS to have expected number of replicas. This change can be reverted when we make the proper fix as
    # mentioned in the above ticket.
    def fn():
        cluster_one_client = member_cluster_clients[0]
        cluster_one_statefulsets = mongodb_multi.read_statefulsets([cluster_one_client])
        return cluster_one_statefulsets[cluster_one_client.cluster_name].status.ready_replicas == 2

    kubetester.wait_until(
        fn, timeout=60, message="Verifying sts has correct number of replicas after scale up in cluster one"
    )

    def fn():
        cluster_two_client = member_cluster_clients[1]
        cluster_two_statefulsets = mongodb_multi.read_statefulsets([cluster_two_client])
        return cluster_two_statefulsets[cluster_two_client.cluster_name].status.ready_replicas == 1

    kubetester.wait_until(
        fn, timeout=60, message="Verifying sts has correct number of replicas after scale up in cluster two"
    )

    def fn():
        cluster_three_client = member_cluster_clients[2]
        cluster_three_statefulsets = mongodb_multi.read_statefulsets([cluster_three_client])
        return cluster_three_statefulsets[cluster_three_client.cluster_name].status.ready_replicas == 2

    kubetester.wait_until(
        fn, timeout=60, message="Verifying sts has correct number of replicas after scale up in cluster three"
    )


def test_ops_manager_has_been_updated_correctly_after_scaling():
    ac = AutomationConfigTester()
    ac.assert_processes_size(5)


def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti | MongoDB, ca_path: str):
    tester = mongodb_multi.tester()
    tester.assert_connectivity(opts=[with_tls(use_tls=True, ca_path=ca_path)])
