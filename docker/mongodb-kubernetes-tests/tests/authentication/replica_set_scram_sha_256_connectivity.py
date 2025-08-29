from typing import Dict

from kubetester import (
    create_or_update_secret,
    create_secret,
    find_fixture,
    read_secret,
    update_secret,
    wait_until,
)
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.phase import Phase
from pytest import fixture, mark

MDB_RESOURCE = "my-replica-set"
USER_NAME = "mms-user-1"
PASSWORD_SECRET_NAME = "mms-user-1-password"
CONNECTION_STRING_SECRET_NAME = "my-replica-set-connection-string"
USER_PASSWORD = "my-password"
USER_DATABASE = "admin"


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("replica-set-scram-sha-256.yaml"),
        namespace=namespace,
        name=MDB_RESOURCE,
    )
    resource.set_version(custom_mdb_version)

    resource["spec"]["security"]["authentication"] = {
        "ignoreUnknownUsers": True,
        "enabled": True,
        "modes": ["SCRAM"],
    }

    return resource.update()


@fixture(scope="function")
def scram_user(namespace: str) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(find_fixture("scram-sha-user.yaml"), namespace=namespace)

    create_or_update_secret(
        KubernetesTester.get_namespace(),
        resource.get_secret_name(),
        {"password": USER_PASSWORD},
    )

    return resource.update()


@fixture(scope="function")
def standard_secret(replica_set: MongoDB):
    secret_name = "{}-{}-{}".format(replica_set.name, USER_NAME, USER_DATABASE)
    return read_secret(replica_set.namespace, secret_name)


@fixture(scope="function")
def connection_string_secret(replica_set: MongoDB):
    return read_secret(replica_set.namespace, CONNECTION_STRING_SECRET_NAME)


@mark.e2e_replica_set_scram_sha_256_user_connectivity
class TestReplicaSetCreation(KubernetesTester):
    def test_replica_set_created(self, replica_set: MongoDB):
        replica_set.assert_reaches_phase(Phase.Running, timeout=400)

    def test_replica_set_connectivity(self, replica_set: MongoDB):
        replica_set.assert_connectivity()

    def test_ops_manager_state_correctly_updated(self, replica_set: MongoDB):
        tester = replica_set.get_automation_config_tester()

        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
        tester.assert_authentication_enabled()
        tester.assert_expected_users(0)
        tester.assert_authoritative_set(False)


@mark.e2e_replica_set_scram_sha_256_user_connectivity
def test_create_user(scram_user: MongoDBUser):
    scram_user.assert_reaches_phase(Phase.Updated)


@mark.e2e_replica_set_scram_sha_256_user_connectivity
class TestReplicaSetIsUpdatedWithNewUser(KubernetesTester):
    def test_replica_set_connectivity(self, replica_set: MongoDB):
        replica_set.assert_connectivity()

    def test_ops_manager_state_correctly_updated(self, replica_set: MongoDB):
        expected_roles = {
            (USER_DATABASE, "clusterAdmin"),
            (USER_DATABASE, "userAdminAnyDatabase"),
            (USER_DATABASE, "readWrite"),
            (USER_DATABASE, "userAdminAnyDatabase"),
        }

        tester = replica_set.get_automation_config_tester()
        tester.assert_has_user(USER_NAME)
        tester.assert_user_has_roles(USER_NAME, expected_roles)
        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
        tester.assert_authentication_enabled()
        tester.assert_expected_users(1)
        tester.assert_authoritative_set(False)

    def test_user_can_authenticate_with_correct_password(self, replica_set: MongoDB):
        replica_set.tester().assert_scram_sha_authentication(
            password="my-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-256",
        )

    def test_user_cannot_authenticate_with_incorrect_password(self, replica_set: MongoDB):
        replica_set.tester().assert_scram_sha_authentication_fails(
            password="invalid-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-256",
        )


@mark.e2e_replica_set_scram_sha_256_user_connectivity
class TestCanChangePassword(KubernetesTester):
    def test_user_can_authenticate_with_new_password(self, namespace: str, replica_set: MongoDB):
        update_secret(namespace, PASSWORD_SECRET_NAME, {"password": "my-new-password7"})
        replica_set.tester().assert_scram_sha_authentication(
            password="my-new-password7",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-256",
        )

    def test_user_cannot_authenticate_with_old_password(self, replica_set: MongoDB):
        replica_set.tester().assert_scram_sha_authentication_fails(
            password="my-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-256",
        )


@mark.e2e_replica_set_scram_sha_256_user_connectivity
def test_credentials_secret_is_created(replica_set: MongoDB, standard_secret: Dict[str, str]):
    assert "username" in standard_secret
    assert "password" in standard_secret
    assert "connectionString.standard" in standard_secret
    assert "connectionString.standardSrv" in standard_secret


@mark.e2e_replica_set_scram_sha_256_user_connectivity
def test_credentials_can_connect_to_db(replica_set: MongoDB, standard_secret: Dict[str, str]):
    print("Connecting with {}".format(standard_secret["connectionString.standard"]))
    replica_set.assert_connectivity_from_connection_string(standard_secret["connectionString.standard"], tls=False)


@mark.e2e_replica_set_scram_sha_256_user_connectivity
def test_credentials_can_connect_to_db_with_srv(replica_set: MongoDB, standard_secret: Dict[str, str]):
    print("Connecting with {}".format(standard_secret["connectionString.standardSrv"]))
    replica_set.assert_connectivity_from_connection_string(standard_secret["connectionString.standardSrv"], tls=False)


@mark.e2e_replica_set_scram_sha_256_user_connectivity
def test_update_user_with_connection_string_secret(scram_user: MongoDBUser):
    scram_user.load()
    scram_user["spec"]["connectionStringSecretName"] = CONNECTION_STRING_SECRET_NAME
    scram_user.update()

    scram_user.assert_reaches_phase(Phase.Updated)


@mark.e2e_replica_set_scram_sha_256_user_connectivity
def test_credentials_can_connect_to_db_with_connection_string_secret(
    replica_set: MongoDB, connection_string_secret: Dict[str, str]
):
    print("Connecting with {}".format(connection_string_secret["connectionString.standard"]))
    replica_set.assert_connectivity_from_connection_string(
        connection_string_secret["connectionString.standard"], tls=False
    )

    print("Connecting with {}".format(connection_string_secret["connectionString.standardSrv"]))
    replica_set.assert_connectivity_from_connection_string(
        connection_string_secret["connectionString.standardSrv"], tls=False
    )


@mark.e2e_replica_set_scram_sha_256_user_connectivity
def test_authentication_is_still_configured_after_remove_authentication(namespace: str, replica_set: MongoDB):
    replica_set["spec"]["security"]["authentication"] = None
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    def ac_updated() -> bool:
        tester = replica_set.get_automation_config_tester()
        # authentication remains enabled as the operator is not configuring it when
        # spec.security.authentication is not configured
        try:
            tester.assert_has_user(USER_NAME)
            tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
            tester.assert_authentication_enabled()
            tester.assert_expected_users(1)
            tester.assert_authoritative_set(False)
            return True
        except AssertionError:
            return False

    wait_until(ac_updated, timeout=600)


@mark.e2e_replica_set_scram_sha_256_user_connectivity
def test_authentication_can_be_disabled_without_modes(namespace: str, replica_set: MongoDB):
    replica_set["spec"]["security"]["authentication"] = {
        "enabled": False,
    }
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    def auth_disabled() -> bool:
        tester = replica_set.get_automation_config_tester()
        # we have explicitly set authentication to be disabled
        try:
            tester.assert_has_user(USER_NAME)
            tester.assert_authentication_disabled(remaining_users=1)
            return True
        except AssertionError:
            return False

    wait_until(auth_disabled, timeout=600)
