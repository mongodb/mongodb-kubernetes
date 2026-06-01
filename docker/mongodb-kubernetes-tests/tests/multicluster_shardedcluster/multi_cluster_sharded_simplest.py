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
from tests.conftest import get_member_cluster_names
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
    resource["spec"]["mongodbResourceRef"]["namespace"] = namespace
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
def test_enable_scram(sharded_cluster: MongoDB):
    sharded_cluster["spec"]["security"] = {
        "authentication": {
            "agents": {"mode": "MONGODB-CR"},
            "enabled": True,
            "modes": ["SCRAM-SHA-1", "SCRAM-SHA-256", "MONGODB-CR"],
        }
    }
    sharded_cluster.update()
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=600)


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
    mongodb_user.assert_reaches_phase(Phase.Updated, timeout=300)


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
def test_connection_string_secret_has_controller_ref_on_central_cluster(
    namespace: str,
    mongodb_user: MongoDBUser,
    central_cluster_client: kubernetes.client.ApiClient,
):
    """The central cluster connection string Secret must carry a controller owner
    reference pointing to the MongoDBUser CR so that Kubernetes GC removes it when the
    MongoDBUser is deleted. Member cluster secrets intentionally omit owner references
    to prevent cross-cluster GC."""
    secret_name = f"{MDB_RESOURCE_NAME}-{USER_RESOURCE}-{USER_DATABASE}"
    v1 = kubernetes.client.CoreV1Api(central_cluster_client)
    secret = v1.read_namespaced_secret(name=secret_name, namespace=namespace)

    owner_refs = secret.metadata.owner_references or []
    controller_ref = next((ref for ref in owner_refs if ref.controller), None)

    assert (
        controller_ref is not None
    ), f"Connection string secret '{secret_name}' on the central cluster has no controller owner reference."
    assert (
        controller_ref.name == mongodb_user.name
    ), f"Expected controller owner '{mongodb_user.name}', got '{controller_ref.name}'."
    assert (
        controller_ref.kind == "MongoDBUser"
    ), f"Expected controller owner kind 'MongoDBUser', got '{controller_ref.kind}'."


@mark.e2e_multi_cluster_sharded_simplest
def test_connection_string_secret_deleted_on_user_deletion(
    namespace: str,
    mongodb_user: MongoDBUser,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
):
    """Delete the MongoDBUser CR and verify that the connection string Secret is removed
    from every member cluster and from the central cluster. Member cluster secrets carry no
    ownerReferences (to avoid cross-cluster GC) so the operator deletes them explicitly
    through its finalizer. The central cluster Secret carries a controller owner reference
    and is removed by Kubernetes GC once the MongoDBUser CR is deleted."""
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

    wait_until(wait_for_user_deleted, timeout=300)

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
                f"Unexpected error reading secret '{secret_name}' from cluster " f"'{mc_client.cluster_name}': {e}"
            )

    # The central cluster Secret is owned by the MongoDBUser CR via a controller owner
    # reference and is removed by Kubernetes GC once the MongoDBUser is deleted.
    v1 = kubernetes.client.CoreV1Api(central_cluster_client)

    def central_secret_deleted() -> bool:
        try:
            v1.read_namespaced_secret(name=secret_name, namespace=namespace)
            return False
        except kubernetes.client.ApiException as e:
            return e.status == 404

    wait_until(central_secret_deleted, timeout=30)
