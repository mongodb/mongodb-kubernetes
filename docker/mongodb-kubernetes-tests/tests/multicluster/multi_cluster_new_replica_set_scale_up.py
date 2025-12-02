from typing import List

import kubernetes
import kubetester
import pytest
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongotester import with_tls
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.multicluster.conftest import cluster_spec_list

RESOURCE_NAME = "multi-replica-set"


@pytest.fixture(scope="module")
def mongodb_multi(
    namespace: str,
    multi_cluster_issuer_ca_configmap: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
    custom_mdb_version: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("mongodb-multi-new.yaml"), RESOURCE_NAME, namespace)
    if kubetester.try_load(resource):
        return resource
    resource.set_version(custom_mdb_version)
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [1, 1, 1])
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource.update()


@pytest.mark.e2e_multi_cluster_new_replica_set_scale_up
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@pytest.mark.e2e_multi_cluster_new_replica_set_scale_up
def test_create_mongodb_multi(mongodb_multi: MongoDB):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_multi_cluster_new_replica_set_scale_up
def test_statefulsets_have_been_created_correctly(
    mongodb_multi: MongoDB,
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


# TODO: uncomment when scaling is fixed
@pytest.mark.e2e_multi_cluster_new_replica_set_scale_up
def test_scale_mongodb_multi(mongodb_multi: MongoDB):
    mongodb_multi.load()
    mongodb_multi["spec"]["clusterSpecList"][0]["members"] = 2
    mongodb_multi["spec"]["clusterSpecList"][1]["members"] = 1
    mongodb_multi["spec"]["clusterSpecList"][2]["members"] = 2
    mongodb_multi.update()

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1800)


@pytest.mark.e2e_multi_cluster_new_replica_set_scale_up
def test_statefulsets_have_been_scaled_up_correctly(
    mongodb_multi: MongoDB,
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
