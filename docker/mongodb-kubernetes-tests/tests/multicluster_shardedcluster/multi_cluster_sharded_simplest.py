from typing import List

import kubernetes
from kubetester import create_or_update_secret, find_fixture, read_secret, try_load, wait_until
from kubetester.kubetester import ensure_ent_version
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.conftest import get_member_cluster_clients, get_member_cluster_names
from tests.multicluster.conftest import cluster_spec_list

logger = test_logger.get_test_logger(__name__)

MDB_RESOURCE_NAME = "sh"
USER_RESOURCE = "sh-user-1"
USER_NAME = "my-user-1"
USER_DATABASE = "admin"
PASSWORD_SECRET_NAME = "sh-user-1-password"
USER_PASSWORD = "my-password"


@fixture(scope="module")
def mongodb_user(central_cluster_client: kubernetes.client.ApiClient, namespace: str) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(find_fixture("mongodb-user.yaml"), USER_RESOURCE, namespace)
    resource["spec"]["username"] = USER_NAME
    resource["spec"]["passwordSecretKeyRef"] = {"name": PASSWORD_SECRET_NAME, "key": "password"}
    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource


@fixture(scope="module")
def sharded_cluster(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("sharded-cluster-multi-cluster.yaml"), namespace=namespace, name=MDB_RESOURCE_NAME
    )

    try_load(resource)
    return resource


@mark.e2e_multi_cluster_sharded_simplest
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_sharded_simplest
def test_create(sharded_cluster: MongoDB, custom_mdb_version: str, issuer_ca_configmap: str):
    sharded_cluster.set_version(ensure_ent_version(custom_mdb_version))

    sharded_cluster["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [2, 2, 1])
    sharded_cluster["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [2, 2, 1])
    sharded_cluster["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 2, 1])
    sharded_cluster.set_architecture_annotation()
    sharded_cluster.update()


@mark.e2e_multi_cluster_sharded_simplest
def test_sharded_cluster(sharded_cluster: MongoDB):
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_multi_cluster_sharded_simplest
def test_statefulsets_multi_cluster_identity(namespace: str):
    """Regression test: sharded cluster StatefulSets in member clusters must carry no
    ownerReferences and must carry the MongoDBMultiResource annotation.

    No ownerReferences: a cross-cluster ownerReference points to the MongoDBShardedCluster
    CR that only exists in the central cluster. The Kubernetes GC treats the StatefulSet as
    an orphan and deletes it immediately, causing an infinite create-delete reconciliation loop.
    Cleanup on CR deletion is handled through explicit label-based deletion instead.

    MongoDBMultiResource annotation: replaces ownerReferences as the identifier that watch
    predicates and the OM connection factory use to map StatefulSets back to their parent CR."""
    for mcc in get_member_cluster_clients():
        sts_list = mcc.list_namespaced_stateful_sets(namespace)
        for sts in sts_list.items:
            owner_refs = sts.metadata.owner_references
            assert not owner_refs, (
                f"StatefulSet {sts.metadata.name} in cluster {mcc.cluster_name} must have no "
                f"ownerReferences in multi-cluster mode, but got: {owner_refs}"
            )
            annotation_value = (sts.metadata.annotations or {}).get("MongoDBMultiResource")
            assert annotation_value == MDB_RESOURCE_NAME, (
                f"StatefulSet {sts.metadata.name} in cluster {mcc.cluster_name} must carry "
                f"annotation 'MongoDBMultiResource={MDB_RESOURCE_NAME}', but got: {annotation_value!r}"
            )


@mark.e2e_multi_cluster_sharded_simplest
def test_create_mongodb_user(
    central_cluster_client: kubernetes.client.ApiClient,
    mongodb_user: MongoDBUser,
    namespace: str,
):
    create_or_update_secret(
        namespace=namespace,
        name=PASSWORD_SECRET_NAME,
        data={"password": USER_PASSWORD},
        api_client=central_cluster_client,
    )
    mongodb_user.update()
    mongodb_user.assert_reaches_phase(Phase.Updated, timeout=120)


@mark.e2e_multi_cluster_sharded_simplest
def test_connection_string_secret_was_created(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    secret_name = f"{MDB_RESOURCE_NAME}-{USER_RESOURCE}-{USER_DATABASE}"
    for mc_client in member_cluster_clients:
        secret_data = read_secret(namespace, secret_name, api_client=mc_client.api_client)
        assert "username" in secret_data
        assert "password" in secret_data
        assert "connectionString.standard" in secret_data
        assert "connectionString.standardSrv" in secret_data


@mark.e2e_multi_cluster_sharded_simplest
def test_connection_string_secret_deleted_on_user_deletion(
    namespace: str,
    mongodb_user: MongoDBUser,
    member_cluster_clients: List[MultiClusterClient],
):
    """Delete the MongoDBUser CR and verify that the connection string Secret is removed
    from every member cluster. In multi-cluster mode these secrets carry no ownerReferences
    (to avoid cross-cluster GC), so the operator must delete them explicitly through its
    finalizer."""
    mongodb_user.delete()

    def wait_for_user_deleted() -> bool:
        try:
            mongodb_user.load()
            return False
        except kubernetes.client.ApiException as e:
            if e.status == 404:
                return True
            logger.error(e)
            return False

    wait_until(wait_for_user_deleted, timeout=120)

    secret_name = f"{MDB_RESOURCE_NAME}-{USER_RESOURCE}-{USER_DATABASE}"
    for mc_client in member_cluster_clients:
        try:
            read_secret(namespace, secret_name, api_client=mc_client.api_client)
            raise AssertionError(
                f"Connection string secret '{secret_name}' still exists in cluster "
                f"'{mc_client.cluster_name}' after MongoDBUser deletion."
            )
        except kubernetes.client.ApiException as e:
            assert e.status == 404, (
                f"Unexpected error reading secret '{secret_name}' from cluster "
                f"'{mc_client.cluster_name}': {e}"
            )
