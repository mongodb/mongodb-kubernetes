import kubernetes
import pytest

from kubetester import create_or_update
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.kubetester import (
    fixture as yaml_fixture,
    skip_if_local,
)
from kubetester.operator import Operator
from tests.multicluster.conftest import cluster_spec_list

MDBM_RESOURCE = "multi-replica-set-upgrade"


@pytest.fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: list[str],
) -> MongoDBMulti:

    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), MDBM_RESOURCE, namespace)
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])
    resource["spec"]["version"] = "4.4.11-ent"
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    return create_or_update(resource)


@pytest.mark.e2e_multi_cluster_upgrade_downgrade
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@skip_if_local
@pytest.mark.e2e_multi_cluster_upgrade_downgrade
def test_create_mongodb_multi_running(mongodb_multi: MongoDBMulti):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)
    mongodb_multi.tester().assert_version("4.4.11-ent")


@skip_if_local
@pytest.mark.e2e_multi_cluster_upgrade_downgrade
def test_mongodb_multi_upgrade(mongodb_multi: MongoDBMulti):
    mongodb_multi.load()
    mongodb_multi["spec"]["version"] = "5.0.5-ent"
    mongodb_multi["spec"]["featureCompatibilityVersion"] = "4.4"
    mongodb_multi.update()

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)

    mongodb_multi.tester().assert_version("5.0.5-ent")


@skip_if_local
@pytest.mark.e2e_multi_cluster_upgrade_downgrade
def test_upgraded_replica_set_is_reachable(mongodb_multi: MongoDBMulti):
    tester = mongodb_multi.tester()
    tester.assert_connectivity()


@skip_if_local
@pytest.mark.e2e_multi_cluster_upgrade_downgrade
def test_mongodb_multi_downgrade(mongodb_multi: MongoDBMulti):
    mongodb_multi.load()
    mongodb_multi["spec"]["version"] = "4.4.11-ent"
    mongodb_multi["spec"]["featureCompatibilityVersion"] = None
    mongodb_multi.update()

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)
    mongodb_multi.tester().assert_version("4.4.11-ent")


@skip_if_local
@pytest.mark.e2e_multi_cluster_upgrade_downgrade
def test_downgraded_replica_set_is_reachable(mongodb_multi: MongoDBMulti):
    tester = mongodb_multi.tester()
    tester.assert_connectivity()
