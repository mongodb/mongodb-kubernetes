from typing import List
from pytest import mark, fixture

from kubetester.mongodb import Phase
import kubernetes
from kubetester.kubetester import fixture as yaml_fixture, KubernetesTester
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.operator import Operator


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient, namespace: str
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi-cluster.yaml"), "multi-replica-set", namespace
    )

    # override agent startup flags
    resource["spec"]["agent"] = {
        "startupOptions": {"logFile": "/var/log/mongodb-mms-automation/customLogFile"}
    }

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource.create()


@mark.e2e_multi_cluster_agent_flags
def test_create_mongodb_multi(
    multi_cluster_operator: Operator, mongodb_multi: MongoDBMulti
):
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
