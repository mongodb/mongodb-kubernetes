from typing import List

import kubernetes
import kubetester
import pytest
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongotester import with_tls
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.multicluster.conftest import cluster_spec_list

RESOURCE_NAME = "multi-replica-set"
BUNDLE_SECRET_NAME = f"prefix-{RESOURCE_NAME}-cert"


@pytest.fixture(scope="module")
def mongodb_multi_unmarshalled(
    namespace: str,
    multi_cluster_issuer_ca_configmap: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), RESOURCE_NAME, namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])

    resource["spec"]["security"] = {
        "certsSecretPrefix": "prefix",
        "tls": {
            "ca": multi_cluster_issuer_ca_configmap,
        },
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource


@pytest.fixture(scope="module")
def server_certs(
    multi_cluster_issuer: str,
    mongodb_multi_unmarshalled: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):

    return create_multi_cluster_mongodb_tls_certs(
        multi_cluster_issuer,
        BUNDLE_SECRET_NAME,
        member_cluster_clients,
        central_cluster_client,
        mongodb_multi_unmarshalled,
    )


@pytest.fixture(scope="module")
def mongodb_multi(mongodb_multi_unmarshalled: MongoDBMulti, server_certs: str) -> MongoDBMulti:
    # we have created certs for all 5 members, but want to start at only 3.
    mongodb_multi_unmarshalled["spec"]["clusterSpecList"][0]["members"] = 1
    mongodb_multi_unmarshalled["spec"]["clusterSpecList"][1]["members"] = 1
    mongodb_multi_unmarshalled["spec"]["clusterSpecList"][2]["members"] = 1
    return mongodb_multi_unmarshalled.create()


@pytest.mark.e2e_multi_cluster_replica_set_scale_up
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@pytest.mark.e2e_multi_cluster_replica_set_scale_up
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_multi_cluster_replica_set_scale_up
def test_statefulsets_have_been_created_correctly(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    # even though we already verified, in previous test, that the MongoDBMultiCluster resource's phase is running (that would mean all STSs are ready)
    # checking the expected number of replicas for STS makes the result flaky. Details are documented here https://jira.mongodb.org/browse/CLOUDP-329231
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


@pytest.mark.e2e_multi_cluster_replica_set_scale_up
def test_ops_manager_has_been_updated_correctly_before_scaling():
    ac = AutomationConfigTester()
    ac.assert_processes_size(3)


@pytest.mark.e2e_multi_cluster_replica_set_scale_up
def test_scale_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.load()
    mongodb_multi["spec"]["clusterSpecList"][0]["members"] = 2
    mongodb_multi["spec"]["clusterSpecList"][1]["members"] = 1
    mongodb_multi["spec"]["clusterSpecList"][2]["members"] = 2
    mongodb_multi.update()

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1800)


@pytest.mark.e2e_multi_cluster_replica_set_scale_up
def test_statefulsets_have_been_scaled_up_correctly(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    # even though we already verified, in previous test, that the MongoDBMultiCluster resource's phase is running (that would mean all STSs are ready)
    # checking the expected number of replicas for STS makes the result flaky. Details are documented here https://jira.mongodb.org/browse/CLOUDP-329231
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


@pytest.mark.e2e_multi_cluster_replica_set_scale_up
def test_ops_manager_has_been_updated_correctly_after_scaling():
    ac = AutomationConfigTester()
    ac.assert_processes_size(5)


@skip_if_local
@pytest.mark.e2e_multi_cluster_replica_set_scale_up
def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti, ca_path: str):
    tester = mongodb_multi.tester()
    tester.assert_connectivity(opts=[with_tls(use_tls=True, ca_path=ca_path)])
