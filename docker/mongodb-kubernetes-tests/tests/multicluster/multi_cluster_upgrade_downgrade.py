import kubernetes
import pymongo
import pytest
from kubetester import try_load
from kubetester.kubetester import ensure_ent_version, fcv_from_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongotester import MongoDBBackgroundTester
from kubetester.operator import Operator
from tests.multicluster.conftest import cluster_spec_list

MDBM_RESOURCE = "multi-replica-set-upgrade"


@pytest.fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: list[str],
    custom_mdb_prev_version: str,
) -> MongoDBMulti:

    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), MDBM_RESOURCE, namespace)
    resource.set_version(ensure_ent_version(custom_mdb_prev_version))
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])
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


@pytest.mark.e2e_multi_cluster_upgrade_downgrade
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@pytest.mark.e2e_multi_cluster_upgrade_downgrade
def test_create_mongodb_multi_running(mongodb_multi: MongoDBMulti, custom_mdb_prev_version: str):
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)
    mongodb_multi.tester().assert_version(ensure_ent_version(custom_mdb_prev_version))


@pytest.mark.e2e_multi_cluster_upgrade_downgrade
def test_start_background_checker(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.start()


@pytest.mark.e2e_multi_cluster_upgrade_downgrade
def test_mongodb_multi_upgrade(mongodb_multi: MongoDBMulti, custom_mdb_prev_version: str, custom_mdb_version: str):
    mongodb_multi.load()
    mongodb_multi["spec"]["version"] = ensure_ent_version(custom_mdb_version)
    mongodb_multi["spec"]["featureCompatibilityVersion"] = fcv_from_version(custom_mdb_prev_version)
    mongodb_multi.update()

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)

    mongodb_multi.tester().assert_version(ensure_ent_version(custom_mdb_version))


@pytest.mark.e2e_multi_cluster_upgrade_downgrade
def test_upgraded_replica_set_is_reachable(mongodb_multi: MongoDBMulti):
    tester = mongodb_multi.tester()
    tester.assert_connectivity()


@pytest.mark.e2e_multi_cluster_upgrade_downgrade
def test_mongodb_multi_downgrade(mongodb_multi: MongoDBMulti, custom_mdb_prev_version: str):
    mongodb_multi.load()
    mongodb_multi["spec"]["version"] = ensure_ent_version(custom_mdb_prev_version)
    mongodb_multi["spec"]["featureCompatibilityVersion"] = fcv_from_version(custom_mdb_prev_version)
    mongodb_multi.update()

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)
    mongodb_multi.tester().assert_version(ensure_ent_version(custom_mdb_prev_version))


@pytest.mark.e2e_multi_cluster_upgrade_downgrade
def test_downgraded_replica_set_is_reachable(mongodb_multi: MongoDBMulti):
    tester = mongodb_multi.tester()
    tester.assert_connectivity()


@pytest.mark.e2e_multi_cluster_upgrade_downgrade
def test_mdb_healthy_throughout_change_version(
    mdb_health_checker: MongoDBBackgroundTester,
):
    mdb_health_checker.assert_healthiness()
