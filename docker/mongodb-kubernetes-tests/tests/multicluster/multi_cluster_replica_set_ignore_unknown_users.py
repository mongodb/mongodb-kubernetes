import kubernetes
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.multicluster.conftest import cluster_spec_list


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDBMulti:

    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi.yaml"),
        "multi-replica-set",
        namespace,
    )
    resource.set_version(custom_mdb_version)

    resource["spec"]["security"] = {"authentication": {"enabled": True, "modes": ["SCRAM"]}}

    resource["spec"]["security"]["authentication"]["ignoreUnknownUsers"] = True
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    return resource.update()


@mark.e2e_multi_cluster_replica_set_ignore_unknown_users
def test_replica_set(multi_cluster_operator: Operator, mongodb_multi: MongoDBMulti):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=800)


@mark.e2e_multi_cluster_replica_set_ignore_unknown_users
def test_authoritative_set_false(mongodb_multi: MongoDBMulti):
    tester = AutomationConfigTester(KubernetesTester.get_automation_config())
    tester.assert_authoritative_set(False)


@mark.e2e_multi_cluster_replica_set_ignore_unknown_users
def test_set_ignore_unknown_users_false(mongodb_multi: MongoDBMulti):
    mongodb_multi.load()
    mongodb_multi["spec"]["security"]["authentication"]["ignoreUnknownUsers"] = False
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=800)


@mark.e2e_multi_cluster_replica_set_ignore_unknown_users
def test_authoritative_set_true(mongodb_multi: MongoDBMulti):
    tester = AutomationConfigTester(KubernetesTester.get_automation_config())
    tester.assert_authoritative_set(True)
