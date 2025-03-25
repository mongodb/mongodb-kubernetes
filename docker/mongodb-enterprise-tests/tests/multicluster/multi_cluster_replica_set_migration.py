from typing import List

import kubernetes
import pymongo
import pytest
from kubetester import try_load
from kubetester.kubetester import assert_statefulset_architecture
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import get_static_containers_architecture
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.mongotester import MongoDBBackgroundTester
from kubetester.operator import Operator
from tests.multicluster.conftest import cluster_spec_list

MDBM_RESOURCE = "multi-replica-set-migration"


@pytest.fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: list[str],
    custom_mdb_version,
) -> MongoDBMulti:

    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), MDBM_RESOURCE, namespace)
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])
    resource["spec"]["version"] = custom_mdb_version
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    try_load(resource)
    return resource


@pytest.fixture(scope="module")
def mdb_health_checker(mongodb_multi: MongoDBMulti) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(
        mongodb_multi.tester(),
        allowed_sequential_failures=1,
        health_function_params={
            "attempts": 1,
            "write_concern": pymongo.WriteConcern(w="majority"),
        },
    )


@pytest.mark.e2e_multi_cluster_replica_set_migration
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@pytest.mark.e2e_multi_cluster_replica_set_migration
def test_create_mongodb_multi_running(mongodb_multi: MongoDBMulti):
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)


@pytest.mark.e2e_multi_cluster_replica_set_migration
def test_start_background_checker(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.start()


@pytest.mark.e2e_multi_cluster_replica_set_migration
def test_migrate_architecture(mongodb_multi: MongoDBMulti, member_cluster_clients: List[MultiClusterClient]):
    """
    If the E2E is running with default architecture as non-static,
    then the test will migrate to static and vice versa.
    """
    original_default_architecture = get_static_containers_architecture()
    target_architecture = "non-static" if original_default_architecture == "static" else "static"

    mongodb_multi.trigger_architecture_migration()

    mongodb_multi.load()
    assert mongodb_multi["metadata"]["annotations"]["mongodb.com/v1.architecture"] == target_architecture

    mongodb_multi.assert_abandons_phase(Phase.Running, timeout=1000)
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1000)

    statefulsets = mongodb_multi.read_statefulsets(member_cluster_clients)
    for statefulset in statefulsets.values():
        assert_statefulset_architecture(statefulset, target_architecture)


@pytest.mark.e2e_multi_cluster_replica_set_migration
def test_mdb_healthy_throughout_change_version(
    mdb_health_checker: MongoDBBackgroundTester,
):
    mdb_health_checker.assert_healthiness()
