"""TLS-free fork of tests/multicluster/multi_cluster_replica_set_scale_up.py (M7 Tier 2).

The original is blocked on TLS, which the decentralized POC guards against; stripping the
security block and the with_tls connectivity opts loses nothing this exercise measures — the
point is live coverage of the scale-up invariants: AC-first ordering, growing a deployment from
3 members to 5 across all three member clusters, and the STS ready-replica retry loop needed for
CLOUDP-329231. Keep the test bodies diffable against the original.
"""

from typing import List

import kubernetes
import kubetester
import pytest
from kubetester import try_load
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.multicluster.conftest import cluster_spec_list

RESOURCE_NAME = "multi-replica-set"


@pytest.fixture(scope="module")
def mongodb_multi(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), RESOURCE_NAME, namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    # start at only 3 members total; the rest are added during scale up.
    resource["spec"]["clusterSpecList"][0]["members"] = 1
    resource["spec"]["clusterSpecList"][1]["members"] = 1
    resource["spec"]["clusterSpecList"][2]["members"] = 1
    try_load(resource)
    return resource


@pytest.mark.e2e_multi_cluster_decentralized_replica_set_scale_up
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


@pytest.mark.e2e_multi_cluster_decentralized_replica_set_scale_up
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_multi_cluster_decentralized_replica_set_scale_up
def test_statefulsets_have_been_created_correctly(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    # Even though we already verified, in previous test, that the MongoDBMultiCluster resource's phase is running (that would mean all STSs are ready);
    # checking the expected number of replicas for STS makes the test flaky because of an issue mentioned in detail in this ticket https://jira.mongodb.org/browse/CLOUDP-329231.
    # That's why we are waiting for STS to have expected number of replicas. This change can be reverted when we make the proper fix as
    # mentioned in the above ticket.
    def fn_cluster_one():
        cluster_one_client = member_cluster_clients[0]
        cluster_one_statefulsets = mongodb_multi.read_statefulsets([cluster_one_client])
        return cluster_one_statefulsets[cluster_one_client.cluster_name].status.ready_replicas == 1

    kubetester.wait_until(
        fn_cluster_one, timeout=60, message="Verifying sts has correct number of replicas in cluster one"
    )

    def fn_cluster_two():
        cluster_two_client = member_cluster_clients[1]
        cluster_two_statefulsets = mongodb_multi.read_statefulsets([cluster_two_client])
        return cluster_two_statefulsets[cluster_two_client.cluster_name].status.ready_replicas == 1

    kubetester.wait_until(
        fn_cluster_two, timeout=60, message="Verifying sts has correct number of replicas in cluster two"
    )

    def fn_cluster_three():
        cluster_three_client = member_cluster_clients[2]
        cluster_three_statefulsets = mongodb_multi.read_statefulsets([cluster_three_client])
        return cluster_three_statefulsets[cluster_three_client.cluster_name].status.ready_replicas == 1

    kubetester.wait_until(
        fn_cluster_three, timeout=60, message="Verifying sts has correct number of replicas in cluster three"
    )


@pytest.mark.e2e_multi_cluster_decentralized_replica_set_scale_up
def test_ops_manager_has_been_updated_correctly_before_scaling():
    ac = AutomationConfigTester()
    ac.assert_processes_size(3)


@pytest.mark.e2e_multi_cluster_decentralized_replica_set_scale_up
def test_scale_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.load()
    mongodb_multi["spec"]["clusterSpecList"][0]["members"] = 2
    mongodb_multi["spec"]["clusterSpecList"][1]["members"] = 1
    mongodb_multi["spec"]["clusterSpecList"][2]["members"] = 2
    mongodb_multi.update()

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1800)


@pytest.mark.e2e_multi_cluster_decentralized_replica_set_scale_up
def test_statefulsets_have_been_scaled_up_correctly(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    # Even though we already verified, in previous test, that the MongoDBMultiCluster resource's phase is running (that would mean all STSs are ready);
    # checking the expected number of replicas for STS makes the test flaky because of an issue mentioned in detail in this ticket https://jira.mongodb.org/browse/CLOUDP-329231.
    # That's why we are waiting for STS to have expected number of replicas. This change can be reverted when we make the proper fix as
    # mentioned in the above ticket.
    def fn_cluster_one():
        cluster_one_client = member_cluster_clients[0]
        cluster_one_statefulsets = mongodb_multi.read_statefulsets([cluster_one_client])
        return cluster_one_statefulsets[cluster_one_client.cluster_name].status.ready_replicas == 2

    kubetester.wait_until(
        fn_cluster_one, timeout=60, message="Verifying sts has correct number of replicas after scale up in cluster one"
    )

    def fn_cluster_two():
        cluster_two_client = member_cluster_clients[1]
        cluster_two_statefulsets = mongodb_multi.read_statefulsets([cluster_two_client])
        return cluster_two_statefulsets[cluster_two_client.cluster_name].status.ready_replicas == 1

    kubetester.wait_until(
        fn_cluster_two, timeout=60, message="Verifying sts has correct number of replicas after scale up in cluster two"
    )

    def fn_cluster_three():
        cluster_three_client = member_cluster_clients[2]
        cluster_three_statefulsets = mongodb_multi.read_statefulsets([cluster_three_client])
        return cluster_three_statefulsets[cluster_three_client.cluster_name].status.ready_replicas == 2

    kubetester.wait_until(
        fn_cluster_three,
        timeout=60,
        message="Verifying sts has correct number of replicas after scale up in cluster three",
    )


@pytest.mark.e2e_multi_cluster_decentralized_replica_set_scale_up
def test_ops_manager_has_been_updated_correctly_after_scaling():
    ac = AutomationConfigTester()
    ac.assert_processes_size(5)


@skip_if_local
@pytest.mark.e2e_multi_cluster_decentralized_replica_set_scale_up
def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti):
    tester = mongodb_multi.tester()
    tester.assert_connectivity()
