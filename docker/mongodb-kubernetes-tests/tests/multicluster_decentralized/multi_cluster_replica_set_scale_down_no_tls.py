"""TLS-free fork of tests/multicluster/multi_cluster_replica_set_scale_down.py (M7 Tier 2).

The original is blocked on TLS, which the decentralized POC guards against; stripping the
security block and the with_tls connectivity opts loses nothing this exercise measures — the
point is live coverage of the scale-down invariants: AC-first ordering, one-cluster-at-a-time
advancement, and scaling a member cluster to zero (CLOUDP-324655) under the leader's ladder.
Keep the test bodies diffable against the original.
"""

from typing import List

import kubernetes
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
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), RESOURCE_NAME, namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


@pytest.mark.e2e_multi_cluster_decentralized_replica_set_scale_down
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


@pytest.mark.e2e_multi_cluster_decentralized_replica_set_scale_down
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


@pytest.mark.e2e_multi_cluster_decentralized_replica_set_scale_down
def test_statefulsets_have_been_created_correctly(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    statefulsets = mongodb_multi.read_statefulsets(member_cluster_clients)
    cluster_one_client = member_cluster_clients[0]
    cluster_one_sts = statefulsets[cluster_one_client.cluster_name]
    assert cluster_one_sts.status.ready_replicas == 2

    cluster_two_client = member_cluster_clients[1]
    cluster_two_sts = statefulsets[cluster_two_client.cluster_name]
    assert cluster_two_sts.status.ready_replicas == 1

    cluster_three_client = member_cluster_clients[2]
    cluster_three_sts = statefulsets[cluster_three_client.cluster_name]
    assert cluster_three_sts.status.ready_replicas == 2


@pytest.mark.e2e_multi_cluster_decentralized_replica_set_scale_down
def test_ops_manager_has_been_updated_correctly_before_scaling():
    ac = AutomationConfigTester()
    ac.assert_processes_size(5)


@pytest.mark.e2e_multi_cluster_decentralized_replica_set_scale_down
def test_scale_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.load()
    mongodb_multi["spec"]["clusterSpecList"][0]["members"] = 1
    # Testing scaling down to zero is required to test fix for https://jira.mongodb.org/browse/CLOUDP-324655
    mongodb_multi["spec"]["clusterSpecList"][1]["members"] = 0
    mongodb_multi["spec"]["clusterSpecList"][2]["members"] = 2
    mongodb_multi.update()

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1800)


@pytest.mark.e2e_multi_cluster_decentralized_replica_set_scale_down
def test_statefulsets_have_been_scaled_down_correctly(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    statefulsets = mongodb_multi.read_statefulsets(member_cluster_clients)
    cluster_one_client = member_cluster_clients[0]
    cluster_one_sts = statefulsets[cluster_one_client.cluster_name]
    assert cluster_one_sts.status.ready_replicas == 1

    cluster_two_client = member_cluster_clients[1]
    cluster_two_sts = statefulsets[cluster_two_client.cluster_name]
    assert cluster_two_sts.status.ready_replicas is None

    cluster_three_client = member_cluster_clients[2]
    cluster_three_sts = statefulsets[cluster_three_client.cluster_name]
    assert cluster_three_sts.status.ready_replicas == 2


@pytest.mark.e2e_multi_cluster_decentralized_replica_set_scale_down
def test_ops_manager_has_been_updated_correctly_after_scaling():
    ac = AutomationConfigTester()
    ac.assert_processes_size(3)


@skip_if_local
@pytest.mark.e2e_multi_cluster_decentralized_replica_set_scale_down
def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti):
    tester = mongodb_multi.tester()
    tester.assert_connectivity()
