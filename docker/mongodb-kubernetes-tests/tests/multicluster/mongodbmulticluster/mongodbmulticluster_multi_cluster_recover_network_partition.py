from typing import List

import kubernetes
from kubeobject import CustomObject
from kubernetes import client
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.operator import Operator
from pytest import fixture, mark
from tests.multicluster.conftest import cluster_spec_list

from ..shared import multi_cluster_recover_network_partition as testhelper

RESOURCE_NAME = "multi-replica-set"


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: client.ApiClient,
    namespace: str,
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodbmulticluster-multi.yaml"), RESOURCE_NAME, namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["persistent"] = False
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])
    resource.api = client.CustomObjectsApi(central_cluster_client)

    return resource


@mark.e2e_mongodbmulticluster_multi_cluster_recover_network_partition
def test_label_namespace(namespace: str, central_cluster_client: client.ApiClient):
    testhelper.test_label_namespace(namespace, central_cluster_client)


@mark.e2e_mongodbmulticluster_multi_cluster_recover_network_partition
def test_create_service_entry(service_entries: List[CustomObject]):
    testhelper.test_create_service_entry(service_entries)


@mark.e2e_mongodbmulticluster_multi_cluster_recover_network_partition
def test_deploy_operator(multi_cluster_operator_manual_remediation: Operator):
    testhelper.test_deploy_operator(multi_cluster_operator_manual_remediation)


@mark.e2e_mongodbmulticluster_multi_cluster_recover_network_partition
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    testhelper.test_create_mongodb_multi(mongodb_multi)


@mark.e2e_mongodbmulticluster_multi_cluster_recover_network_partition
def test_update_service_entry_block_failed_cluster_traffic(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
):
    testhelper.test_update_service_entry_block_failed_cluster_traffic(
        namespace, central_cluster_client, member_cluster_names
    )


@mark.e2e_mongodbmulticluster_multi_cluster_recover_network_partition
def test_delete_database_statefulset_in_failed_cluster(
    mongodb_multi: MongoDBMulti,
    member_cluster_names: list[str],
):
    testhelper.test_delete_database_statefulset_in_failed_cluster(mongodb_multi, member_cluster_names)


@mark.e2e_mongodbmulticluster_multi_cluster_recover_network_partition
def test_mongodb_multi_enters_failed_state(
    mongodb_multi: MongoDBMulti,
    namespace: str,
    central_cluster_client: client.ApiClient,
):
    testhelper.test_mongodb_multi_enters_failed_state(mongodb_multi, namespace, central_cluster_client)


@mark.e2e_mongodbmulticluster_multi_cluster_recover_network_partition
def test_recover_operator_remove_cluster(
    member_cluster_names: List[str],
    namespace: str,
    central_cluster_client: client.ApiClient,
):
    testhelper.test_recover_operator_remove_cluster(member_cluster_names, namespace, central_cluster_client)


@mark.e2e_mongodbmulticluster_multi_cluster_recover_network_partition
def test_mongodb_multi_recovers_removing_cluster(mongodb_multi: MongoDBMulti, member_cluster_names: List[str]):
    testhelper.test_mongodb_multi_recovers_removing_cluster(mongodb_multi, member_cluster_names)
