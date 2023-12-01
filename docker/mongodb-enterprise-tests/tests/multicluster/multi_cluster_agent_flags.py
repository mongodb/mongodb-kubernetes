from typing import List

import kubernetes
from kubetester import create_or_update
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.operator import Operator
from pytest import fixture, mark
from tests.multicluster.conftest import cluster_spec_list


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: list[str],
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi-cluster.yaml"), "multi-replica-set", namespace)
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])

    # override agent startup flags
    resource["spec"]["agent"] = {"startupOptions": {"logFile": "/var/log/mongodb-mms-automation/customLogFile"}}
    resource["spec"]["agent"]["logLevel"] = "DEBUG"

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return create_or_update(resource)


@mark.e2e_multi_cluster_agent_flags
def test_create_mongodb_multi(multi_cluster_operator: Operator, mongodb_multi: MongoDBMulti):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)


@mark.e2e_multi_cluster_agent_flags
def test_multi_replicaset_has_agent_flags(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    cluster_1_client = member_cluster_clients[0]
    cmd = [
        "/bin/sh",
        "-c",
        "ls /var/log/mongodb-mms-automation/customLogFile* | wc -l",
    ]
    result = KubernetesTester.run_command_in_pod_container(
        "multi-replica-set-0-0",
        namespace,
        cmd,
        container="mongodb-enterprise-database",
        api_client=cluster_1_client.api_client,
    )
    assert result != "0"
