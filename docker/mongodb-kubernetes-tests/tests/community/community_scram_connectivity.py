from typing import Dict

from kubernetes import client
from kubernetes.client.exceptions import ApiException
from kubetester import create_or_update_secret, read_secret, try_load, update_secret, wait_until
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb_community import MongoDBCommunity
from kubetester.mongotester import assert_connection_string_with_mongosh
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger

logger = test_logger.get_test_logger(__name__)

MDB_RESOURCE = "community-scram-rs"
USER_NAME = "my-user"
USER_PASSWORD = "my-user-password-value"
ROTATED_USER_PASSWORD = "my-user-new-password-value"
USER_PASSWORD_SECRET = "my-user-password"
USER_DATABASE = "admin"

APP_USER_NAME = "app-user"
APP_USER_PASSWORD = "app-user-password-value"
APP_USER_PASSWORD_SECRET = "app-user-password"
APP_CONNECTION_STRING_DATABASE = "myapp"

CUSTOM_CONNECTION_STRING_SECRET_NAME = "custom-conn-secret-name"


def user_secret_name(username: str, database: str) -> str:
    # generated name order: <resource>-<username>-<database>
    return "{}-{}-{}".format(MDB_RESOURCE, username, database)


def secret_is_deleted(namespace: str, secret_name: str) -> bool:
    try:
        read_secret(namespace, secret_name)
        return False
    except ApiException as e:
        return e.status == 404


@fixture(scope="function")
def mdbc(namespace: str) -> MongoDBCommunity:
    resource = MongoDBCommunity.from_yaml(
        yaml_fixture("community-replicaset-scram-users.yaml"),
        namespace=namespace,
    )

    try_load(resource)
    return resource


@fixture(scope="function")
def standard_secret(mdbc: MongoDBCommunity) -> Dict[str, str]:
    return read_secret(mdbc.namespace, user_secret_name(USER_NAME, USER_DATABASE))


@fixture(scope="function")
def app_standard_secret(mdbc: MongoDBCommunity) -> Dict[str, str]:
    return read_secret(mdbc.namespace, user_secret_name(APP_USER_NAME, USER_DATABASE))


@mark.e2e_community_scram_connectivity
def test_install_operator(default_operator: Operator):
    default_operator.wait_for_operator_ready()


@mark.e2e_community_scram_connectivity
def test_install_secrets(namespace: str):
    create_or_update_secret(namespace=namespace, name=USER_PASSWORD_SECRET, data={"password": USER_PASSWORD})
    create_or_update_secret(namespace=namespace, name=APP_USER_PASSWORD_SECRET, data={"password": APP_USER_PASSWORD})


@mark.e2e_community_scram_connectivity
def test_replicaset_running(mdbc: MongoDBCommunity):
    mdbc.update()
    mdbc.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_community_scram_connectivity
def test_credentials_secret_is_created(standard_secret: Dict[str, str]):
    assert standard_secret["username"] == USER_NAME
    assert standard_secret["password"] == USER_PASSWORD
    assert "connectionString.standard" in standard_secret
    assert "connectionString.standardSrv" in standard_secret
    # authSource must match the user's spec.db
    assert f"authSource={USER_DATABASE}" in standard_secret["connectionString.standard"]
    assert f"authSource={USER_DATABASE}" in standard_secret["connectionString.standardSrv"]
    assert "ssl=false" in standard_secret["connectionString.standard"]
    assert "ssl=false" in standard_secret["connectionString.standardSrv"]


@mark.e2e_community_scram_connectivity
def test_credentials_can_connect_to_db(standard_secret: Dict[str, str]):
    conn = standard_secret["connectionString.standard"]
    assert_connection_string_with_mongosh(conn, expect_success=True, eval_script="db.runCommand({ping: 1})")


@mark.e2e_community_scram_connectivity
def test_credentials_can_connect_to_db_with_srv(standard_secret: Dict[str, str]):
    conn = standard_secret["connectionString.standardSrv"]
    assert_connection_string_with_mongosh(conn, expect_success=True, eval_script="db.runCommand({ping: 1})")


@mark.e2e_community_scram_connectivity
def test_user_cannot_authenticate_with_wrong_password(standard_secret: Dict[str, str]):
    conn = standard_secret["connectionString.standard"]
    wrong_conn = conn.replace(f"{USER_NAME}:{USER_PASSWORD}@", f"{USER_NAME}:wrong-password@", 1)
    assert wrong_conn != conn
    assert_connection_string_with_mongosh(wrong_conn, expect_success=False, eval_script="db.runCommand({ping: 1})")


@mark.e2e_community_scram_connectivity
def test_user_without_connection_string_database_cannot_insert(standard_secret: Dict[str, str]):
    """The URI path is empty for users without connectionStringDatabase, so mongosh
    defaults to the "test" database and the user has no roles there."""
    for key in ("connectionString.standard", "connectionString.standardSrv"):
        assert_connection_string_with_mongosh(standard_secret[key], expect_success=False)


@mark.e2e_community_scram_connectivity
def test_connection_string_database_secret_is_created(app_standard_secret: Dict[str, str]):
    for key in ("connectionString.standard", "connectionString.standardSrv"):
        conn = app_standard_secret[key]
        # authSource must reflect spec.db, not connectionStringDatabase
        assert f"authSource={USER_DATABASE}" in conn
        # the URI path segment must reflect connectionStringDatabase
        assert f"/{APP_CONNECTION_STRING_DATABASE}?" in conn


@mark.e2e_community_scram_connectivity
def test_app_user_can_connect_and_insert(app_standard_secret: Dict[str, str]):
    """The URI path database receives the insert since the user holds readWrite there."""
    for key in ("connectionString.standard", "connectionString.standardSrv"):
        assert_connection_string_with_mongosh(app_standard_secret[key], expect_success=True)


@mark.e2e_community_scram_connectivity
def test_connection_string_secret_has_controller_owner_ref(namespace: str):
    """The connection string secret must carry a controller owner reference pointing
    to the MongoDBCommunity resource so Kubernetes GC removes it on deletion."""
    secret = client.CoreV1Api().read_namespaced_secret(
        name=user_secret_name(USER_NAME, USER_DATABASE),
        namespace=namespace,
    )

    owner_refs = secret.metadata.owner_references or []
    controller_ref = next((ref for ref in owner_refs if ref.controller), None)

    assert controller_ref is not None, "connection string secret has no controller owner reference"
    assert controller_ref.name == MDB_RESOURCE
    assert controller_ref.kind == "MongoDBCommunity"

    # spec.users[].connectionStringSecretAnnotations must be carried over
    assert secret.metadata.annotations["environment"] == "e2e"


@mark.e2e_community_scram_connectivity
def test_password_rotation_updates_connection_string_secret(namespace: str, standard_secret: Dict[str, str]):
    """Rotating the password secret must update the connection string secret: the old
    password stops working and the published URIs carry the new one."""
    old_conn = standard_secret["connectionString.standard"]

    update_secret(namespace, USER_PASSWORD_SECRET, {"password": ROTATED_USER_PASSWORD})

    wait_until(
        lambda: read_secret(namespace, user_secret_name(USER_NAME, USER_DATABASE))["password"] == ROTATED_USER_PASSWORD,
        timeout=120,
    )

    def old_password_rejected() -> bool:
        try:
            assert_connection_string_with_mongosh(
                old_conn, expect_success=False, eval_script="db.runCommand({ping: 1})"
            )
            return True
        except AssertionError:
            return False

    wait_until(old_password_rejected, timeout=120)

    new_conn = read_secret(namespace, user_secret_name(USER_NAME, USER_DATABASE))["connectionString.standard"]
    assert f"{USER_NAME}:{ROTATED_USER_PASSWORD}@" in new_conn
    assert_connection_string_with_mongosh(new_conn, expect_success=True, eval_script="db.runCommand({ping: 1})")


@mark.e2e_community_scram_connectivity
def test_custom_connection_string_secret_name(namespace: str, mdbc: MongoDBCommunity):
    """Setting spec.users[].connectionStringSecretName moves the secret: the custom
    secret holds the connection strings and the generated one is cleaned up."""
    generated_secret_name = user_secret_name(USER_NAME, USER_DATABASE)

    mdbc.load()
    mdbc["spec"]["users"][0]["connectionStringSecretName"] = CUSTOM_CONNECTION_STRING_SECRET_NAME
    mdbc.update()
    mdbc.assert_reaches_phase(Phase.Running, timeout=300)

    def custom_secret_ready() -> bool:
        try:
            data = read_secret(namespace, CUSTOM_CONNECTION_STRING_SECRET_NAME)
        except ApiException:
            return False
        return (
            "connectionString.standard" in data and f"authSource={USER_DATABASE}" in data["connectionString.standard"]
        )

    wait_until(custom_secret_ready, timeout=120)

    wait_until(lambda: secret_is_deleted(namespace, generated_secret_name), timeout=120)


@mark.e2e_community_scram_connectivity
def test_connection_string_secrets_are_deleted_with_resource(namespace: str, mdbc: MongoDBCommunity):
    """Deleting the MongoDBCommunity removes the connection string secrets through
    the controller owner references."""
    mdbc.delete()

    for secret_name in (CUSTOM_CONNECTION_STRING_SECRET_NAME, user_secret_name(APP_USER_NAME, USER_DATABASE)):
        run_periodically(
            lambda name=secret_name: secret_is_deleted(namespace, name),
            timeout=120,
            msg=f"connection string secret {secret_name} to be GC'd",
        )
