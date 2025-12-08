from typing import List

import kubernetes
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDB
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from pytest import fixture, mark
from tests.multicluster.conftest import cluster_spec_list

from ..shared import multi_cluster_agent_flags as testhelper

MDB_RESOURCE = "multi-replica-set"


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("mongodb-multi-cluster.yaml"), MDB_RESOURCE, namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])

    # override agent startup flags
    resource["spec"]["agent"] = {"startupOptions": {"logFile": "/var/log/mongodb-mms-automation/customLogFile"}}
    resource["spec"]["agent"]["logLevel"] = "DEBUG"

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource.update()


@mark.e2e_mongodb_multi_cluster_agent_flags
def test_create_mongodb_multi(multi_cluster_operator: Operator, mongodb_multi: MongoDB):
    testhelper.test_create_mongodb_multi(multi_cluster_operator, mongodb_multi)


@mark.e2e_mongodb_multi_cluster_agent_flags
def test_multi_replicaset_has_agent_flags(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    testhelper.test_multi_replicaset_has_agent_flags(namespace, member_cluster_clients)


@mark.e2e_mongodb_multi_cluster_agent_flags
def test_placeholders_in_external_services(
    namespace: str,
    mongodb_multi: MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    testhelper.test_placeholders_in_external_services(namespace, mongodb_multi, member_cluster_clients)
