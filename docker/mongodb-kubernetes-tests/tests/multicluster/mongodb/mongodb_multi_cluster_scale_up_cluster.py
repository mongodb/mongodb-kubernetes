from typing import List

import kubernetes
import pytest
from kubetester import (
    create_or_update_configmap,
    random_k8s_name,
    read_configmap,
    try_load,
)
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from tests.multicluster.conftest import cluster_spec_list

from ..shared import multi_cluster_scale_up_cluster as testhelper

RESOURCE_NAME = "multi-replica-set"
BUNDLE_SECRET_NAME = f"prefix-{RESOURCE_NAME}-cert"


@pytest.fixture(scope="module")
def project_name_prefix(namespace: str) -> str:
    return random_k8s_name(f"{namespace}-project-")


@pytest.fixture(scope="module")
def new_project_configmap(namespace: str, project_name_prefix: str) -> str:
    cm = read_configmap(namespace=namespace, name="my-project")
    project_name = f"{project_name_prefix}-new-project"
    return create_or_update_configmap(
        namespace=namespace,
        name=project_name,
        data={
            "baseUrl": cm["baseUrl"],
            "projectName": project_name,
            "orgId": cm["orgId"],
        },
    )


@pytest.fixture(scope="module")
def mongodb_multi_unmarshalled(
    namespace: str,
    multi_cluster_issuer_ca_configmap: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("mongodb-multi.yaml"), RESOURCE_NAME, namespace)
    resource.set_version(custom_mdb_version)
    # ensure certs are created for the members during scale up
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [3, 1, 2])
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
    mongodb_multi_unmarshalled: MongoDB,
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


@pytest.fixture(scope="function")
def mongodb_multi(mongodb_multi_unmarshalled: MongoDB, server_certs: str) -> MongoDB:
    if try_load(mongodb_multi_unmarshalled):
        return mongodb_multi_unmarshalled

    # remove the last element, we are only starting with 2 clusters we will scale up the 3rd one later.
    mongodb_multi_unmarshalled["spec"]["clusterSpecList"].pop()
    # remove one member from the first cluster to start with 2 members
    mongodb_multi_unmarshalled["spec"]["clusterSpecList"][0]["members"] = 2
    return mongodb_multi_unmarshalled


@pytest.mark.e2e_mongodb_multi_cluster_scale_up_cluster
def test_deploy_operator(multi_cluster_operator: Operator):
    testhelper.test_deploy_operator(multi_cluster_operator)


@pytest.mark.e2e_mongodb_multi_cluster_scale_up_cluster
def test_create_mongodb_multi(mongodb_multi: MongoDB):
    testhelper.test_create_mongodb_multi(mongodb_multi)


@pytest.mark.e2e_mongodb_multi_cluster_scale_up_cluster
def test_statefulsets_have_been_created_correctly(
    mongodb_multi: MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    testhelper.test_statefulsets_have_been_created_correctly(mongodb_multi, member_cluster_clients)


@pytest.mark.e2e_mongodb_multi_cluster_scale_up_cluster
def test_ops_manager_has_been_updated_correctly_before_scaling():
    testhelper.test_ops_manager_has_been_updated_correctly_before_scaling()


@pytest.mark.e2e_mongodb_multi_cluster_scale_up_cluster
def test_scale_mongodb_multi(mongodb_multi: MongoDB, member_cluster_clients: List[MultiClusterClient]):
    testhelper.test_scale_mongodb_multi(mongodb_multi, member_cluster_clients)


@pytest.mark.e2e_mongodb_multi_cluster_scale_up_cluster
def test_statefulsets_have_been_scaled_up_correctly(
    mongodb_multi: MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    testhelper.test_statefulsets_have_been_scaled_up_correctly(mongodb_multi, member_cluster_clients)


@pytest.mark.e2e_mongodb_multi_cluster_scale_up_cluster
def test_ops_manager_has_been_updated_correctly_after_scaling():
    testhelper.test_ops_manager_has_been_updated_correctly_after_scaling()


@skip_if_local
@pytest.mark.e2e_mongodb_multi_cluster_scale_up_cluster
def test_replica_set_is_reachable(mongodb_multi: MongoDB, ca_path: str):
    testhelper.test_replica_set_is_reachable(mongodb_multi, ca_path)


# From here on, the tests are for verifying that we can change the project of the MongoDB resource even with
# non-sequential member ids in the replicaset.


@pytest.mark.e2e_mongodb_multi_cluster_scale_up_cluster
class TestNonSequentialMemberIdsInReplicaSet(KubernetesTester):

    def test_scale_up_first_cluster(self, mongodb_multi: MongoDB, member_cluster_clients: List[MultiClusterClient]):
        testhelper.TestNonSequentialMemberIdsInReplicaSet.test_scale_up_first_cluster(
            mongodb_multi, member_cluster_clients
        )

    def test_change_project(self, mongodb_multi: MongoDB, new_project_configmap: str):
        testhelper.TestNonSequentialMemberIdsInReplicaSet.test_change_project(mongodb_multi, new_project_configmap)
