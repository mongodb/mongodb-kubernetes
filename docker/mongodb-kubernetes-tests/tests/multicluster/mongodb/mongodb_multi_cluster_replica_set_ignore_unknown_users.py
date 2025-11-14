import kubernetes
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from pytest import fixture, mark
from tests.multicluster.conftest import cluster_spec_list

from ..shared import multi_cluster_replica_set_ignore_unknown_users as testhelper

MDB_RESOURCE = "multi-replica-set"


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDB:

    resource = MongoDB.from_yaml(
        yaml_fixture("mongodb-multi.yaml"),
        MDB_RESOURCE,
        namespace,
    )
    resource.set_version(custom_mdb_version)

    resource["spec"]["security"] = {"authentication": {"enabled": True, "modes": ["SCRAM"]}}

    resource["spec"]["security"]["authentication"]["ignoreUnknownUsers"] = True
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    return resource.update()


@mark.e2e_mongodb_multi_cluster_replica_set_ignore_unknown_users
def test_replica_set(multi_cluster_operator: Operator, mongodb_multi: MongoDB):
    testhelper.test_replica_set(multi_cluster_operator, mongodb_multi)


@mark.e2e_mongodb_multi_cluster_replica_set_ignore_unknown_users
def test_authoritative_set_false(mongodb_multi: MongoDB):
    testhelper.test_authoritative_set_false(mongodb_multi)


@mark.e2e_mongodb_multi_cluster_replica_set_ignore_unknown_users
def test_set_ignore_unknown_users_false(mongodb_multi: MongoDB):
    testhelper.test_set_ignore_unknown_users_false(mongodb_multi)


@mark.e2e_mongodb_multi_cluster_replica_set_ignore_unknown_users
def test_authoritative_set_true(mongodb_multi: MongoDB):
    testhelper.test_authoritative_set_true(mongodb_multi)
