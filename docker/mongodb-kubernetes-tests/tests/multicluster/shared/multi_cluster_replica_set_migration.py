from typing import List

from kubetester.kubetester import (
    assert_statefulset_architecture,
    get_default_architecture,
)
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongotester import MongoDBBackgroundTester
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase


def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


def test_create_mongodb_multi_running(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)


def test_start_background_checker(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.start()


def test_migrate_architecture(mongodb_multi: MongoDBMulti | MongoDB, member_cluster_clients: List[MultiClusterClient]):
    """
    If the E2E is running with default architecture as non-static,
    then the test will migrate to static and vice versa.
    """
    original_default_architecture = get_default_architecture()
    target_architecture = "non-static" if original_default_architecture == "static" else "static"

    mongodb_multi.trigger_architecture_migration()

    mongodb_multi.load()
    assert mongodb_multi["metadata"]["annotations"]["mongodb.com/v1.architecture"] == target_architecture

    mongodb_multi.assert_abandons_phase(Phase.Running, timeout=1800)
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1800)

    statefulsets = mongodb_multi.read_statefulsets(member_cluster_clients)
    for statefulset in statefulsets.values():
        assert_statefulset_architecture(statefulset, target_architecture)


def test_mdb_healthy_throughout_change_version(
    mdb_health_checker: MongoDBBackgroundTester,
):
    mdb_health_checker.assert_healthiness()
