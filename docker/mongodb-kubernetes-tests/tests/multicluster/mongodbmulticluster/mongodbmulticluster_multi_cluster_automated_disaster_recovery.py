from typing import List

import kubernetes
from kubeobject import CustomObject
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from pytest import fixture, mark
from tests.multicluster.conftest import (
    cluster_spec_list,
)

from ..shared import multi_cluster_automated_disaster_recovery as testhelper

MDB_RESOURCE = "multi-replica-set"


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodbmulticluster-multi.yaml"), MDB_RESOURCE, namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["persistent"] = False
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    return resource


@mark.e2e_mongodbmulticluster_multi_cluster_disaster_recovery
def test_label_namespace(namespace: str, central_cluster_client: kubernetes.client.ApiClient):
    testhelper.test_label_namespace(namespace, central_cluster_client)


@mark.e2e_mongodbmulticluster_multi_cluster_disaster_recovery
def test_create_service_entry(service_entries: List[CustomObject]):
    testhelper.test_create_service_entry(service_entries)


@mark.e2e_mongodbmulticluster_multi_cluster_disaster_recovery
@mark.e2e_mongodbmulticluster_multi_cluster_multi_disaster_recovery
def test_deploy_operator(multi_cluster_operator: Operator):
    testhelper.test_deploy_operator(multi_cluster_operator)


@mark.e2e_mongodbmulticluster_multi_cluster_disaster_recovery
@mark.e2e_mongodbmulticluster_multi_cluster_multi_disaster_recovery
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    testhelper.test_create_mongodb_multi(mongodb_multi)


@mark.e2e_mongodbmulticluster_multi_cluster_disaster_recovery
def test_update_service_entry_block_failed_cluster_traffic(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
):
    testhelper.test_update_service_entry_block_failed_cluster_traffic(
        namespace, central_cluster_client, member_cluster_names
    )


@mark.e2e_mongodbmulticluster_multi_cluster_disaster_recovery
def test_mongodb_multi_leaves_running_state(
    mongodb_multi: MongoDBMulti,
):
    testhelper.test_mongodb_multi_leaves_running_state(mongodb_multi)


@mark.e2e_mongodbmulticluster_multi_cluster_disaster_recovery
def test_delete_database_statefulset_in_failed_cluster(mongodb_multi: MongoDBMulti, member_cluster_names: list[str]):
    testhelper.test_delete_database_statefulset_in_failed_cluster(mongodb_multi, member_cluster_names)


@mark.e2e_mongodbmulticluster_multi_cluster_disaster_recovery
@mark.e2e_mongodbmulticluster_multi_cluster_multi_disaster_recovery
def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti):
    testhelper.test_replica_set_is_reachable(mongodb_multi)


@mark.e2e_mongodbmulticluster_multi_cluster_disaster_recovery
def test_replica_reaches_running(mongodb_multi: MongoDBMulti):
    testhelper.test_replica_reaches_running(mongodb_multi)


@mark.e2e_mongodbmulticluster_multi_cluster_disaster_recovery
@mark.e2e_mongodbmulticluster_multi_cluster_multi_disaster_recovery
def test_number_numbers_in_ac(mongodb_multi: MongoDBMulti):
    testhelper.test_number_numbers_in_ac(mongodb_multi)


@mark.e2e_mongodbmulticluster_multi_cluster_disaster_recovery
def test_sts_count_in_member_cluster(
    mongodb_multi: MongoDBMulti,
    member_cluster_names: list[str],
    member_cluster_clients: List[MultiClusterClient],
):
    testhelper.test_sts_count_in_member_cluster(mongodb_multi, member_cluster_names, member_cluster_clients)
