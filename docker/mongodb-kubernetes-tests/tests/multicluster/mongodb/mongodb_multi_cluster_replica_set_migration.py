from typing import List

import kubernetes
import pymongo
import pytest
from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDB
from kubetester.mongotester import MongoDBBackgroundTester
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.multicluster.conftest import cluster_spec_list

from ..shared import multi_cluster_replica_set_migration as testhelper

MDBM_RESOURCE = "multi-replica-set-migration"


@pytest.fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: list[str],
    custom_mdb_version,
) -> MongoDB:

    resource = MongoDB.from_yaml(yaml_fixture("mongodb-multi.yaml"), MDBM_RESOURCE, namespace)
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])
    resource["spec"]["version"] = custom_mdb_version
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    try_load(resource)
    return resource


@pytest.fixture(scope="module")
def mdb_health_checker(mongodb_multi: MongoDB) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(
        mongodb_multi.tester(),
        allowed_sequential_failures=1,
        health_function_params={
            "attempts": 1,
            "write_concern": pymongo.WriteConcern(w="majority"),
        },
    )


@pytest.mark.e2e_mongodb_multi_cluster_replica_set_migration
def test_deploy_operator(multi_cluster_operator: Operator):
    testhelper.test_deploy_operator(multi_cluster_operator)


@pytest.mark.e2e_mongodb_multi_cluster_replica_set_migration
def test_create_mongodb_multi_running(mongodb_multi: MongoDB):
    testhelper.test_create_mongodb_multi_running(mongodb_multi)


@pytest.mark.e2e_mongodb_multi_cluster_replica_set_migration
def test_start_background_checker(mdb_health_checker: MongoDBBackgroundTester):
    testhelper.test_start_background_checker(mdb_health_checker)


@pytest.mark.e2e_mongodb_multi_cluster_replica_set_migration
def test_migrate_architecture(mongodb_multi: MongoDB, member_cluster_clients: List[MultiClusterClient]):
    testhelper.test_migrate_architecture(mongodb_multi, member_cluster_clients)


@pytest.mark.e2e_mongodb_multi_cluster_replica_set_migration
def test_mdb_healthy_throughout_change_version(
    mdb_health_checker: MongoDBBackgroundTester,
):
    testhelper.test_mdb_healthy_throughout_change_version(mdb_health_checker)
