from typing import List

import kubernetes
import pytest
from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from tests.multicluster.conftest import cluster_spec_list

from ..shared import multi_cluster_replica_set_deletion as testhelper

MDB_RESOURCE = "multi-replica-set"


@pytest.fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodbmulticluster-multi.yaml"), MDB_RESOURCE, namespace)

    if try_load(resource):
        return resource

    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])

    return resource.update()


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_replica_set_deletion
def test_deploy_operator(multi_cluster_operator: Operator):
    testhelper.test_deploy_operator(multi_cluster_operator)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_replica_set_deletion
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    testhelper.test_create_mongodb_multi(mongodb_multi)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_replica_set_deletion
def test_automation_config_has_been_updated(mongodb_multi: MongoDBMulti):
    testhelper.test_automation_config_has_been_updated(mongodb_multi)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_replica_set_deletion
def test_delete_mongodb_multi(mongodb_multi: MongoDBMulti):
    testhelper.test_delete_mongodb_multi(mongodb_multi)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_replica_set_deletion
def test_deployment_has_been_removed_from_automation_config():
    testhelper.test_deployment_has_been_removed_from_automation_config()


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_replica_set_deletion
def test_kubernetes_resources_have_been_cleaned_up(
    mongodb_multi: MongoDBMulti, member_cluster_clients: List[MultiClusterClient]
):
    testhelper.test_kubernetes_resources_have_been_cleaned_up(mongodb_multi, member_cluster_clients)
