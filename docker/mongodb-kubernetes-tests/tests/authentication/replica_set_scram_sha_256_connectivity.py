from typing import Dict, List

import pytest
from kubetester import (
    create_or_update_secret,
    create_secret,
    find_fixture,
    read_secret,
    try_load,
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

OM_SHA256_USER_NAME = "om-user-sha256"
OM_SHA256_USER_PASSWORD_SECRET = "om-user-sha256-password"
OM_SHA256_USER_PASSWORD = "om-sha256-password-1"


def _get_ac_user(ac_tester, username: str) -> dict:
    users = ac_tester.automation_config["auth"]["usersWanted"]
    matches = [u for u in users if u["user"] == username]
    assert matches, f"User {username!r} not found in usersWanted"
    return matches[0]


def _assert_user_mechanisms(ac_tester, username: str, expected: List[str]) -> None:
    user = _get_ac_user(ac_tester, username)
    assert user.get("mechanisms", []) == expected, (
        f"User {username!r} mechanisms: expected {expected}, got {user.get('mechanisms', [])}"
    )


def create_password_secret(namespace: str) -> str:
    create_or_update_secret(
        namespace,
        PASSWORD_SECRET_NAME,
        {"password": USER_PASSWORD},
    )
    return PASSWORD_SECRET_NAME


@fixture(scope="function")
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
    try_load(resource)
    return resource


@fixture(scope="function")
def scram_user(namespace: str) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(find_fixture("scram-sha-user.yaml"), namespace=namespace)

    try_load(resource)
    return resource


@fixture(scope="function")
def standard_secret(replica_set: MongoDB):
    secret_name = "{}-{}-{}".format(replica_set.name, USER_NAME, USER_DATABASE)
    return read_secret(replica_set.namespace, secret_name)


@fixture(scope="function")
def connection_string_secret(replica_set: MongoDB):
    return read_secret(replica_set.namespace, CONNECTION_STRING_SECRET_NAME)


@fixture(scope="function")
def om_user_sha256(namespace: str, replica_set: MongoDB) -> MongoDBUser:
    create_or_update_secret(namespace, OM_SHA256_USER_PASSWORD_SECRET, {"password": OM_SHA256_USER_PASSWORD})
    replica_set.get_om_tester().add_user(
        username=OM_SHA256_USER_NAME,
        database=USER_DATABASE,
        password=OM_SHA256_USER_PASSWORD,
        mechanisms=["SCRAM-SHA-256"],
        roles=[{"role": "readWrite", "db": USER_DATABASE}],
    )
    resource = MongoDBUser.from_yaml(find_fixture("scram-sha-user.yaml"), namespace=namespace, name=OM_SHA256_USER_NAME)
    resource["spec"]["username"] = OM_SHA256_USER_NAME
    resource["spec"]["passwordSecretKeyRef"] = {"name": OM_SHA256_USER_PASSWORD_SECRET, "key": "password"}
    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE
    try_load(resource)
    return resource


@mark.e2e_replica_set_scram_sha_256_user_connectivity
class TestReplicaSetCreation(KubernetesTester):
    def test_replica_set_created(self, replica_set: MongoDB):
        replica_set.update()
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
def test_create_user(scram_user: MongoDBUser, namespace: str):
    create_password_secret(namespace)
    scram_user.update()
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
        ac_version = replica_set.get_automation_config_tester().automation_config["version"]

        new_password = "my-new-password7"
        update_secret(namespace, PASSWORD_SECRET_NAME, {"password": new_password})

        wait_until(lambda: replica_set.get_automation_config_tester().reached_version(ac_version + 1), timeout=800)

        replica_set.tester().assert_scram_sha_authentication(
            password=new_password,
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
class TestK8sUserHasEmptyMechanisms(KubernetesTester):
    def test_k8s_user_mechanisms_empty_in_ac(self, replica_set: MongoDB):
        """K8s-originated user must have mechanisms=[] regardless of which creds are present."""
        tester = replica_set.get_automation_config_tester()
        tester.assert_has_user(USER_NAME)
        _assert_user_mechanisms(tester, USER_NAME, [])

    def test_k8s_user_has_both_creds(self, replica_set: MongoDB):
        """Both SHA-256 and SHA-1 creds must exist even though mechanisms is []."""
        tester = replica_set.get_automation_config_tester()
        user = _get_ac_user(tester, USER_NAME)
        assert user.get("scramSha256Creds"), "scramSha256Creds should be present"
        assert user.get("scramSha1Creds"), "scramSha1Creds should be present"


@mark.e2e_replica_set_scram_sha_256_user_connectivity
class TestScramDisabledAndReenabled(KubernetesTester):
    def test_disable_scram(self, replica_set: MongoDB):
        replica_set["spec"]["security"]["authentication"] = {"enabled": False}
        replica_set.update()
        replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    def test_reenable_scram(self, replica_set: MongoDB):
        replica_set["spec"]["security"]["authentication"] = {
            "ignoreUnknownUsers": True,
            "enabled": True,
            "modes": ["SCRAM"],
        }
        replica_set.update()
        replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    def test_k8s_user_mechanisms_still_empty_after_recovery(self, replica_set: MongoDB):
        _assert_user_mechanisms(replica_set.get_automation_config_tester(), USER_NAME, [])


@mark.e2e_replica_set_scram_sha_256_user_connectivity
def test_om_user_sha256_created(om_user_sha256: MongoDBUser):
    om_user_sha256.update()
    om_user_sha256.assert_reaches_phase(Phase.Updated)


@mark.e2e_replica_set_scram_sha_256_user_connectivity
class TestOMUserSha256OnlyPreserved(KubernetesTester):
    def test_om_user_sha256_only_mechanism_in_ac(self, replica_set: MongoDB):
        tester = replica_set.get_automation_config_tester()
        tester.assert_has_user(OM_SHA256_USER_NAME)
        _assert_user_mechanisms(tester, OM_SHA256_USER_NAME, ["SCRAM-SHA-256"])

    def test_om_user_sha256_has_no_sha1_creds(self, replica_set: MongoDB):
        user = _get_ac_user(replica_set.get_automation_config_tester(), OM_SHA256_USER_NAME)
        assert user.get("scramSha256Creds"), "SHA-256 creds must be present"
        assert not user.get("scramSha1Creds"), "SHA-1 creds must NOT be present"

    def test_om_user_sha256_password_change_preserves_mechanism(self, namespace: str, replica_set: MongoDB):
        ac_version = replica_set.get_automation_config_tester().automation_config["version"]
        new_password = "om-sha256-password-new-1"
        update_secret(namespace, OM_SHA256_USER_PASSWORD_SECRET, {"password": new_password})
        wait_until(
            lambda: replica_set.get_automation_config_tester().reached_version(ac_version + 1),
            timeout=600,
        )
        tester = replica_set.get_automation_config_tester()
        _assert_user_mechanisms(tester, OM_SHA256_USER_NAME, ["SCRAM-SHA-256"])
        assert not _get_ac_user(tester, OM_SHA256_USER_NAME).get("scramSha1Creds"), (
            "SHA-1 creds must NOT appear after password change"
        )
        replica_set.tester().assert_scram_sha_authentication(
            password=new_password,
            username=OM_SHA256_USER_NAME,
            auth_mechanism="SCRAM-SHA-256",
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
