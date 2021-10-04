from typing import List

import kubernetes
import pytest

from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs import create_multi_cluster_mongodb_tls_certs
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.mongotester import with_tls
from kubetester.operator import Operator
from kubetester.kubetester import (
    fixture as yaml_fixture,
    skip_if_local,
)

RESOURCE_NAME = "multi-replica-set"
BUNDLE_SECRET_NAME = f"prefix-{RESOURCE_NAME}-cert"


@pytest.fixture(scope="module")
def mongodb_multi_unmarshalled(
    namespace: str,
    multi_cluster_issuer_ca_configmap: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi.yaml"), RESOURCE_NAME, namespace
    )
    resource["spec"]["clusterSpecList"]["clusterSpecs"][0]["members"] = 2
    resource["spec"]["clusterSpecList"]["clusterSpecs"][1]["members"] = 1
    resource["spec"]["clusterSpecList"]["clusterSpecs"][2]["members"] = 2
    resource["spec"]["security"] = {
        "tls": {
            "enabled": True,
            "secretRef": {"prefix": "prefix"},
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
):

    return create_multi_cluster_mongodb_tls_certs(
        multi_cluster_issuer,
        BUNDLE_SECRET_NAME,
        member_cluster_clients,
        mongodb_multi_unmarshalled,
    )


@pytest.fixture(scope="module")
def mongodb_multi(
    mongodb_multi_unmarshalled: MongoDBMulti, server_certs: str
) -> MongoDBMulti:
    return mongodb_multi_unmarshalled.create()


@pytest.mark.e2e_multi_cluster_scale_down_cluster
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@pytest.mark.e2e_multi_cluster_scale_down_cluster
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_multi_cluster_scale_down_cluster
def test_statefulsets_have_been_created_correctly(
    mongodb_multi: MongoDBMulti,
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


@pytest.mark.e2e_multi_cluster_scale_down_cluster
def test_ops_manager_has_been_updated_correctly_before_scaling():
    ac = AutomationConfigTester()
    ac.assert_processes_size(5)


@pytest.mark.e2e_multi_cluster_scale_down_cluster
def test_scale_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.load()
    # remove last cluster
    mongodb_multi["spec"]["clusterSpecList"]["clusterSpecs"].pop()
    mongodb_multi.update()

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1800, ignore_errors=True)


@pytest.mark.e2e_multi_cluster_scale_down_cluster
def test_statefulsets_have_been_scaled_down_correctly(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    statefulsets = mongodb_multi.read_statefulsets(member_cluster_clients[:-1])

    cluster_one_client = member_cluster_clients[0]
    cluster_one_sts = statefulsets[cluster_one_client.cluster_name]
    assert cluster_one_sts.status.ready_replicas == 2

    # there should only be one statefulset in the second cluster
    cluster_two_client = member_cluster_clients[1]
    cluster_two_sts = statefulsets[cluster_two_client.cluster_name]
    assert cluster_two_sts.status.ready_replicas == 1

    # there should be no statefulsets in the last cluster
    with pytest.raises(kubernetes.client.exceptions.ApiException) as e:
        mongodb_multi.read_statefulsets([member_cluster_clients[2]])
        assert e.value.reason == "Not Found"


@pytest.mark.e2e_multi_cluster_scale_down_cluster
def test_ops_manager_has_been_updated_correctly_after_scaling():
    ac = AutomationConfigTester()
    ac.assert_processes_size(3)


@skip_if_local
@pytest.mark.e2e_multi_cluster_scale_down_cluster
def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti, ca_path: str):
    tester = mongodb_multi.tester()
    tester.assert_connectivity(opts=[with_tls(use_tls=True, ca_path=ca_path)])
