from typing import List

import kubernetes
from kubetester import create_secret, read_secret, try_load, wait_until
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_user import MongoDBUser
from kubetester.mongotester import with_scram, with_tls
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.multicluster.conftest import cluster_spec_list

CERT_SECRET_PREFIX = "clustercert"
MDB_RESOURCE = "multi-cluster-replica-set"
BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert"
USER_NAME = "my-user-1"
USER_RESOURCE = "multi-replica-set-scram-user"
USER_DATABASE = "admin"
PASSWORD_SECRET_NAME = "mms-user-1-password"
USER_PASSWORD = "my-password"


@fixture(scope="module")
def mongodb_multi_unmarshalled(
    namespace: str,
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), MDB_RESOURCE, namespace)
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["clusterSpecList"] = cluster_spec_list(
        member_cluster_names=member_cluster_names, members=[2, 1, 2]
    )

    return resource


@fixture(scope="module")
def server_certs(
    multi_cluster_issuer: str,
    mongodb_multi_unmarshalled: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):

    return create_multi_cluster_mongodb_tls_certs(
        multi_cluster_issuer,
        BUNDLE_SECRET_NAME,
        member_cluster_clients,
        central_cluster_client,
        mongodb_multi_unmarshalled,
    )


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    server_certs: str,
    mongodb_multi_unmarshalled: MongoDBMulti,
    multi_cluster_issuer_ca_configmap: str,
) -> MongoDBMulti:

    resource = mongodb_multi_unmarshalled
    resource["spec"]["security"] = {
        "certsSecretPrefix": CERT_SECRET_PREFIX,
        "tls": {
            "ca": multi_cluster_issuer_ca_configmap,
        },
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


@fixture(scope="module")
def mongodb_user(central_cluster_client: kubernetes.client.ApiClient, namespace: str) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(yaml_fixture("mongodb-user.yaml"), USER_RESOURCE, namespace)

    resource["spec"]["username"] = USER_NAME
    resource["spec"]["passwordSecretKeyRef"] = {
        "name": PASSWORD_SECRET_NAME,
        "key": "password",
    }
    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE
    resource["spec"]["mongodbResourceRef"]["namespace"] = namespace
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


@mark.e2e_multi_cluster_tls_with_scram
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


@mark.e2e_multi_cluster_tls_with_scram
def test_deploy_mongodb_multi_with_tls(
    mongodb_multi: MongoDBMulti,
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_multi_cluster_tls_with_scram
def test_update_mongodb_multi_tls_with_scram(
    mongodb_multi: MongoDBMulti,
    namespace: str,
):
    mongodb_multi.load()
    mongodb_multi["spec"]["security"] = {"authentication": {"enabled": True, "modes": ["SCRAM"]}}
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_multi_cluster_tls_with_scram
def test_create_mongodb_user(
    central_cluster_client: kubernetes.client.ApiClient,
    mongodb_user: MongoDBUser,
    namespace: str,
):
    # create user secret first
    create_secret(
        namespace=namespace,
        name=PASSWORD_SECRET_NAME,
        data={"password": USER_PASSWORD},
        api_client=central_cluster_client,
    )
    mongodb_user.update()
    mongodb_user.assert_reaches_phase(Phase.Updated, timeout=100)


@skip_if_local
@mark.e2e_multi_cluster_tls_with_scram
def test_tls_connectivity(mongodb_multi: MongoDBMulti, ca_path: str):
    tester = mongodb_multi.tester()
    tester.assert_connectivity(opts=[with_tls(use_tls=True, ca_path=ca_path)])


@skip_if_local
@mark.e2e_multi_cluster_tls_with_scram
def test_replica_set_connectivity_with_scram_and_tls(mongodb_multi: MongoDBMulti, ca_path: str):
    tester = mongodb_multi.tester()
    tester.assert_connectivity(
        db="admin",
        opts=[
            with_scram(USER_NAME, USER_PASSWORD),
            with_tls(use_tls=True, ca_path=ca_path),
        ],
    )


@skip_if_local
@mark.e2e_multi_cluster_tls_with_scram
def test_replica_set_connectivity_from_connection_string_standard(
    namespace: str,
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    ca_path: str,
):
    secret_data = read_secret(
        namespace,
        f"{mongodb_multi.name}-{USER_RESOURCE}-{USER_DATABASE}",
        api_client=member_cluster_clients[0].api_client,
    )
    tester = mongodb_multi.tester()
    tester.cnx_string = secret_data["connectionString.standard"]
    tester.assert_connectivity(
        db="admin",
        opts=[
            with_scram(USER_NAME, USER_PASSWORD),
            with_tls(use_tls=True, ca_path=ca_path),
        ],
    )


@skip_if_local
@mark.e2e_multi_cluster_tls_with_scram
def test_replica_set_connectivity_from_connection_string_standard_srv(
    namespace: str,
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    ca_path: str,
):
    secret_data = read_secret(
        namespace,
        f"{mongodb_multi.name}-{USER_RESOURCE}-{USER_DATABASE}",
        api_client=member_cluster_clients[-1].api_client,
    )
    tester = mongodb_multi.tester()
    tester.cnx_string = secret_data["connectionString.standardSrv"]
    tester.assert_connectivity(
        db="admin",
        opts=[
            with_scram(USER_NAME, USER_PASSWORD),
            with_tls(use_tls=True, ca_path=ca_path),
        ],
    )


@mark.e2e_multi_cluster_tls_with_scram
def test_connection_string_secret_has_controller_ref_on_central_cluster(
    namespace: str,
    mongodb_multi: MongoDBMulti,
    mongodb_user: MongoDBUser,
    central_cluster_client: kubernetes.client.ApiClient,
):
    """The central cluster connection string Secret must carry a controller owner
    reference pointing to the MongoDBUser CR so that Kubernetes GC removes it when the
    MongoDBUser is deleted. Member cluster secrets intentionally omit owner references
    to prevent cross-cluster GC."""
    secret_name = f"{mongodb_multi.name}-{USER_RESOURCE}-{USER_DATABASE}"
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


@mark.e2e_multi_cluster_tls_with_scram
def test_connection_string_secret_deleted_on_user_deletion(
    namespace: str,
    mongodb_multi: MongoDBMulti,
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
            return e.status == 404

    wait_until(wait_for_user_deleted, timeout=120)

    secret_name = f"{mongodb_multi.name}-{USER_RESOURCE}-{USER_DATABASE}"
    for mc_client in member_cluster_clients:
        try:
            read_secret(namespace, secret_name, api_client=mc_client.api_client)
            raise AssertionError(
                f"Connection string secret '{secret_name}' still exists in cluster "
                f"'{mc_client.cluster_name}' after MongoDBUser deletion."
            )
        except kubernetes.client.ApiException as e:
            assert (
                e.status == 404
            ), f"Unexpected error reading secret '{secret_name}' from cluster '{mc_client.cluster_name}': {e}"

    v1 = kubernetes.client.CoreV1Api(central_cluster_client)

    def central_secret_deleted() -> bool:
        try:
            v1.read_namespaced_secret(name=secret_name, namespace=namespace)
            return False
        except kubernetes.client.ApiException as e:
            return e.status == 404

    wait_until(central_secret_deleted, timeout=30)


@mark.e2e_multi_cluster_tls_with_scram
def test_mongodb_multi_tls_enable_x509(
    mongodb_multi: MongoDBMulti,
    namespace: str,
):
    mongodb_multi.load()

    mongodb_multi["spec"]["security"]["authentication"]["modes"].append("X509")
    mongodb_multi["spec"]["security"]["authentication"]["agents"] = {"mode": "SCRAM"}
    mongodb_multi.update()

    # sometimes the agents need more time to register than the time we wait ->
    # "Failed to enable Authentication for MongoDB Multi Replicaset"
    # after this the agents eventually succeed.
    mongodb_multi.assert_reaches_phase(Phase.Running, ignore_errors=True, timeout=1000)


@mark.e2e_multi_cluster_tls_with_scram
def test_mongodb_multi_tls_automation_config_was_updated(
    mongodb_multi: MongoDBMulti,
    namespace: str,
):
    tester = AutomationConfigTester(KubernetesTester.get_automation_config())
    tester.assert_authentication_mechanism_enabled("MONGODB-X509", active_auth_mechanism=False)
    tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
    tester.assert_authentication_enabled(expected_num_deployment_auth_mechanisms=2)
