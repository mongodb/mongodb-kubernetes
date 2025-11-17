from typing import Dict, List

import kubernetes
import pytest
from kubernetes import client
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from tests.conftest import (
    setup_log_rotate_for_agents,
)
from tests.multicluster.conftest import cluster_spec_list

from ..shared import multi_cluster_replica_set as testhelper

MONGODB_PORT = 30000
MDB_RESOURCE = "multi-replica-set"


@pytest.fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names,
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodbmulticluster-multi-central-sts-override.yaml"),
        MDB_RESOURCE,
        namespace,
    )
    resource.set_version(custom_mdb_version)
    resource["spec"]["persistent"] = False
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])

    additional_mongod_config = {
        "systemLog": {"logAppend": True, "verbosity": 4},
        "operationProfiling": {"mode": "slowOp"},
        "net": {"port": MONGODB_PORT},
    }

    resource["spec"]["additionalMongodConfig"] = additional_mongod_config
    setup_log_rotate_for_agents(resource)

    # TODO: incorporate this into the base class.
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    resource.set_architecture_annotation()

    resource.update()
    return resource


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_replica_set
def test_create_kube_config_file(cluster_clients: Dict, central_cluster_name: str, member_cluster_names: str):
    testhelper.test_create_kube_config_file(cluster_clients, central_cluster_name, member_cluster_names)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_replica_set
def test_deploy_operator(multi_cluster_operator: Operator):
    testhelper.test_deploy_operator(multi_cluster_operator)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_replica_set
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    testhelper.test_create_mongodb_multi(mongodb_multi)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_replica_set
def test_statefulset_is_created_across_multiple_clusters(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    testhelper.test_statefulset_is_created_across_multiple_clusters(mongodb_multi, member_cluster_clients)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_replica_set
def test_pvc_not_created(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    namespace: str,
):
    testhelper.test_pvc_not_created(mongodb_multi, member_cluster_clients, namespace)


@skip_if_local
@pytest.mark.e2e_mongodbmulticluster_multi_cluster_replica_set
def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti):
    testhelper.test_replica_set_is_reachable(mongodb_multi)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_replica_set
def test_statefulset_overrides(mongodb_multi: MongoDBMulti, member_cluster_clients: List[MultiClusterClient]):
    testhelper.test_statefulset_overrides(mongodb_multi, member_cluster_clients)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_replica_set
def test_headless_service_creation(
    mongodb_multi: MongoDBMulti,
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    testhelper.test_headless_service_creation(mongodb_multi, namespace, member_cluster_clients)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_replica_set
def test_mongodb_options(mongodb_multi: MongoDBMulti):
    testhelper.test_mongodb_options(mongodb_multi)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_replica_set
def test_update_additional_options(mongodb_multi: MongoDBMulti, central_cluster_client: kubernetes.client.ApiClient):
    testhelper.test_update_additional_options(mongodb_multi, central_cluster_client)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_replica_set
def test_mongodb_options_were_updated(mongodb_multi: MongoDBMulti):
    testhelper.test_mongodb_options_were_updated(mongodb_multi)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_replica_set
def test_delete_member_cluster_sts(
    namespace: str,
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    testhelper.test_delete_member_cluster_sts(namespace, mongodb_multi, member_cluster_clients)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_replica_set
def test_cleanup_on_mdbm_delete(mongodb_multi: MongoDBMulti, member_cluster_clients: List[MultiClusterClient]):
    testhelper.test_cleanup_on_mdbm_delete(mongodb_multi, member_cluster_clients)


def assert_container_in_sts(container_name: str, sts: client.V1StatefulSet):
    testhelper.assert_container_in_sts(container_name, sts)
