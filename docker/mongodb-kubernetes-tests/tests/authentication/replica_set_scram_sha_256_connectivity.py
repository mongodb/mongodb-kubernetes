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
from kubetester.scram import (
    assert_creds_preserved,
    assert_user_mechanisms,
    build_scram_user_resource,
    build_sha256_creds,
    get_ac_user,
    seed_user_in_ac,
)
from pytest import fixture, mark

MDB_RESOURCE = "my-replica-set"
USER_NAME = "mms-user-1"
PASSWORD_SECRET_NAME = "mms-user-1-password"
CONNECTION_STRING_SECRET_NAME = "my-replica-set-connection-string"
USER_PASSWORD = "my-password"
USER_DATABASE = "admin"

# User that already exists in Ops Manager with only SHA-256 creds and the SCRAM-SHA-256 mechanism.
OM_SHA256_USER_NAME = "om-user-sha256"
OM_SHA256_USER_PASSWORD = "om-sha256-password-1"
OM_SHA256_USER_PASSWORD_SECRET = "om-user-sha256-password"
SEEDED_SHA256_CREDS = build_sha256_creds(OM_SHA256_USER_PASSWORD)

# User that already exists in Ops Manager with no mechanisms list and only SHA-256 creds.
OM_NO_MECH_USER_NAME = "om-user-no-mech"
OM_NO_MECH_USER_PASSWORD = "om-no-mech-password-1"
OM_NO_MECH_USER_PASSWORD_SECRET = "om-user-no-mech-password"
SEEDED_NO_MECH_SHA256_CREDS = build_sha256_creds(OM_NO_MECH_USER_PASSWORD)

NON_ADMIN_USER_NAME = "mms-user-2"
NON_ADMIN_PASSWORD_SECRET_NAME = "mms-user-2-password"
NON_ADMIN_USER_PASSWORD = "my-password-2"
NON_ADMIN_USER_DATABASE = "testdb"


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
def non_admin_standard_secret(replica_set: MongoDB):
    secret_name = "{}-{}-{}".format(replica_set.name, NON_ADMIN_USER_NAME, NON_ADMIN_USER_DATABASE)
    return read_secret(replica_set.namespace, secret_name)


@fixture(scope="function")
def connection_string_secret(replica_set: MongoDB):
    return read_secret(replica_set.namespace, CONNECTION_STRING_SECRET_NAME)


def _seed_sha256_user_in_ac(replica_set: MongoDB) -> None:
    seed_user_in_ac(
        om_tester=replica_set.get_om_tester(),
        username=OM_SHA256_USER_NAME,
        db=USER_DATABASE,
        roles=[{"role": "readWrite", "db": USER_DATABASE}],
        mechanisms=["SCRAM-SHA-256"],
        sha256_creds=SEEDED_SHA256_CREDS,
    )


def _build_sha256_user_in_k8s(namespace: str) -> MongoDBUser:
    return build_scram_user_resource(
        namespace, OM_SHA256_USER_NAME, OM_SHA256_USER_PASSWORD, OM_SHA256_USER_PASSWORD_SECRET, MDB_RESOURCE
    )


def _seed_no_mech_user_in_ac(replica_set: MongoDB) -> None:
    seed_user_in_ac(
        om_tester=replica_set.get_om_tester(),
        username=OM_NO_MECH_USER_NAME,
        db=USER_DATABASE,
        roles=[{"role": "readWrite", "db": USER_DATABASE}],
        mechanisms=None,
        sha256_creds=SEEDED_NO_MECH_SHA256_CREDS,
    )


def _build_no_mech_user_in_k8s(namespace: str) -> MongoDBUser:
    return build_scram_user_resource(
        namespace, OM_NO_MECH_USER_NAME, OM_NO_MECH_USER_PASSWORD, OM_NO_MECH_USER_PASSWORD_SECRET, MDB_RESOURCE
    )


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
    # authSource in the connection string must match the user's spec.db
    assert f"authSource={USER_DATABASE}" in standard_secret["connectionString.standard"]
    assert f"authSource={USER_DATABASE}" in standard_secret["connectionString.standardSrv"]


@mark.e2e_replica_set_scram_sha_256_user_connectivity
def test_create_non_admin_db_user(replica_set: MongoDB, namespace: str):
    create_or_update_secret(namespace, NON_ADMIN_PASSWORD_SECRET_NAME, {"password": NON_ADMIN_USER_PASSWORD})
    resource = MongoDBUser.from_yaml(find_fixture("scram-sha-user-non-admin-db.yaml"), namespace=namespace)
    resource["spec"]["mongodbResourceRef"]["name"] = replica_set.name
    try_load(resource)
    resource.update()
    resource.assert_reaches_phase(Phase.Updated, timeout=150)


@mark.e2e_replica_set_scram_sha_256_user_connectivity
def test_non_admin_db_credentials_secret_is_created(replica_set: MongoDB, non_admin_standard_secret: Dict[str, str]):
    assert "username" in non_admin_standard_secret
    assert "password" in non_admin_standard_secret
    assert "connectionString.standard" in non_admin_standard_secret
    assert "connectionString.standardSrv" in non_admin_standard_secret
    # authSource in the connection string must match the user's spec.db (non-admin database)
    assert f"authSource={NON_ADMIN_USER_DATABASE}" in non_admin_standard_secret["connectionString.standard"]
    assert f"authSource={NON_ADMIN_USER_DATABASE}" in non_admin_standard_secret["connectionString.standardSrv"]


@mark.e2e_replica_set_scram_sha_256_user_connectivity
def test_credentials_can_connect_to_db(replica_set: MongoDB, standard_secret: Dict[str, str]):
    replica_set.assert_connectivity_from_connection_string(standard_secret["connectionString.standard"], tls=False)


@mark.e2e_replica_set_scram_sha_256_user_connectivity
def test_credentials_can_connect_to_db_with_srv(replica_set: MongoDB, standard_secret: Dict[str, str]):
    replica_set.assert_connectivity_from_connection_string(standard_secret["connectionString.standardSrv"], tls=False)


@mark.e2e_replica_set_scram_sha_256_user_connectivity
def test_non_admin_credentials_can_connect_to_db(replica_set: MongoDB, non_admin_standard_secret: Dict[str, str]):
    replica_set.assert_connectivity_from_connection_string(
        non_admin_standard_secret["connectionString.standard"], tls=False
    )


@mark.e2e_replica_set_scram_sha_256_user_connectivity
def test_non_admin_credentials_can_connect_to_db_with_srv(
    replica_set: MongoDB, non_admin_standard_secret: Dict[str, str]
):
    replica_set.assert_connectivity_from_connection_string(
        non_admin_standard_secret["connectionString.standardSrv"], tls=False
    )


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
    replica_set.assert_connectivity_from_connection_string(
        connection_string_secret["connectionString.standard"], tls=False
    )
    replica_set.assert_connectivity_from_connection_string(
        connection_string_secret["connectionString.standardSrv"], tls=False
    )


@mark.e2e_replica_set_scram_sha_256_user_connectivity
class TestK8sUserHasEmptyMechanisms(KubernetesTester):
    def test_k8s_user_mechanisms_empty_in_ac(self, replica_set: MongoDB):
        """K8s-originated user must have mechanisms=[] regardless of which creds are present."""
        tester = replica_set.get_automation_config_tester()
        tester.assert_has_user(USER_NAME)
        assert_user_mechanisms(tester, USER_NAME, [])

    def test_k8s_user_has_both_creds(self, replica_set: MongoDB):
        """Both SHA-256 and SHA-1 creds must exist even though mechanisms is []."""
        tester = replica_set.get_automation_config_tester()
        user = get_ac_user(tester, USER_NAME)
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
        assert_user_mechanisms(replica_set.get_automation_config_tester(), USER_NAME, [])


# Tests importing a user that already exists in Ops Manager into K8s management.
# The user has only SHA-256 creds. The two setup steps below must run in order:
# step 1 creates the user directly in Ops Manager, step 2 registers it with the operator
# via a MongoDBUser resource. TestOMUserSha256OnlyPreserved then checks that the original
# SHA-256 creds are kept intact and that the operator generates the missing SHA-1 creds.


@mark.e2e_replica_set_scram_sha_256_user_connectivity
def test_seed_sha256_user_in_ac(replica_set: MongoDB):
    _seed_sha256_user_in_ac(replica_set)


@mark.e2e_replica_set_scram_sha_256_user_connectivity
def test_om_user_sha256_created(namespace: str):
    resource = _build_sha256_user_in_k8s(namespace)
    resource.update()
    resource.assert_reaches_phase(Phase.Updated)


@mark.e2e_replica_set_scram_sha_256_user_connectivity
class TestOMUserSha256OnlyPreserved(KubernetesTester):
    def test_om_user_sha256_mechanisms_empty_after_transition(self, replica_set: MongoDB):
        # Once Ops Manager processes the password and the operator reconciles again,
        # it treats the imported user as K8s-managed (mechanisms=[]).
        tester = replica_set.get_automation_config_tester()
        tester.assert_has_user(OM_SHA256_USER_NAME)
        assert_user_mechanisms(tester, OM_SHA256_USER_NAME, [])

    def test_om_user_sha256_creds_preserved_byte_for_byte(self, replica_set: MongoDB):
        # The SHA-256 creds set in Ops Manager before the import must remain unchanged.
        # Only the missing SHA-1 creds should be added.
        assert_creds_preserved(
            replica_set.get_automation_config_tester(),
            OM_SHA256_USER_NAME,
            sha256_creds=SEEDED_SHA256_CREDS,
        )

    def test_om_user_sha256_gets_sha1_creds_after_transition(self, replica_set: MongoDB):
        # After the import the operator generates only the missing SHA-1 creds.
        user = get_ac_user(replica_set.get_automation_config_tester(), OM_SHA256_USER_NAME)
        assert user.get("scramSha256Creds"), "SHA-256 creds must be present"
        assert user.get("scramSha1Creds"), "SHA-1 creds must be present after the follow-up reconcile"

    def test_om_user_sha256_can_authenticate_after_transition(self, replica_set: MongoDB):
        replica_set.tester().assert_scram_sha_authentication(
            password=OM_SHA256_USER_PASSWORD,
            username=OM_SHA256_USER_NAME,
            auth_mechanism="SCRAM-SHA-256",
            attempts=20,
        )

    def test_om_user_sha256_password_can_change(self, namespace: str, replica_set: MongoDB):
        ac_version = replica_set.get_automation_config_tester().automation_config["version"]
        new_password = "om-sha256-password-new-1"
        update_secret(namespace, OM_SHA256_USER_PASSWORD_SECRET, {"password": new_password})

        wait_until(
            lambda: replica_set.get_automation_config_tester().reached_version(ac_version + 1),
            timeout=600,
        )

        assert_user_mechanisms(replica_set.get_automation_config_tester(), OM_SHA256_USER_NAME, [])
        replica_set.tester().assert_scram_sha_authentication(
            password=new_password,
            username=OM_SHA256_USER_NAME,
            auth_mechanism="SCRAM-SHA-256",
        )


# Same import scenario as above, but the user has no mechanisms list set in Ops Manager.
# The operator treats this the same as a K8s-managed user: it keeps the existing SHA-256
# creds and generates the missing SHA-1 creds in the same reconcile pass.


@mark.e2e_replica_set_scram_sha_256_user_connectivity
def test_seed_no_mech_user_in_ac(replica_set: MongoDB):
    _seed_no_mech_user_in_ac(replica_set)


@mark.e2e_replica_set_scram_sha_256_user_connectivity
def test_om_user_no_mech_created(namespace: str):
    resource = _build_no_mech_user_in_k8s(namespace)
    resource.update()
    resource.assert_reaches_phase(Phase.Updated)


@mark.e2e_replica_set_scram_sha_256_user_connectivity
class TestOMUserNullMechanismsIsK8sManaged(KubernetesTester):
    def test_om_user_no_mech_mechanisms_empty_after_transition(self, replica_set: MongoDB):
        # Once Ops Manager processes the password and the operator reconciles again,
        # it treats the imported user as K8s-managed (mechanisms=[]).
        tester = replica_set.get_automation_config_tester()
        tester.assert_has_user(OM_NO_MECH_USER_NAME)
        assert_user_mechanisms(tester, OM_NO_MECH_USER_NAME, [])

    def test_om_user_no_mech_sha256_creds_preserved(self, replica_set: MongoDB):
        # The SHA-256 creds set in Ops Manager before the import must remain unchanged.
        # Only the missing SHA-1 creds should be added.
        assert_creds_preserved(
            replica_set.get_automation_config_tester(),
            OM_NO_MECH_USER_NAME,
            sha256_creds=SEEDED_NO_MECH_SHA256_CREDS,
        )

    def test_om_user_no_mech_gets_sha1_creds_after_transition(self, replica_set: MongoDB):
        # After the import the operator generates only the missing SHA-1 creds.
        user = get_ac_user(replica_set.get_automation_config_tester(), OM_NO_MECH_USER_NAME)
        assert user.get("scramSha256Creds"), "SHA-256 creds must be present"
        assert user.get("scramSha1Creds"), "SHA-1 creds must be present after the follow-up reconcile"

    def test_om_user_no_mech_can_authenticate_after_transition(self, replica_set: MongoDB):
        replica_set.tester().assert_scram_sha_authentication(
            password=OM_NO_MECH_USER_PASSWORD,
            username=OM_NO_MECH_USER_NAME,
            auth_mechanism="SCRAM-SHA-256",
            attempts=20,
        )

    def test_om_user_no_mech_password_can_change(self, namespace: str, replica_set: MongoDB):
        ac_version = replica_set.get_automation_config_tester().automation_config["version"]
        new_password = "om-no-mech-password-new-1"
        update_secret(namespace, OM_NO_MECH_USER_PASSWORD_SECRET, {"password": new_password})

        wait_until(
            lambda: replica_set.get_automation_config_tester().reached_version(ac_version + 1),
            timeout=600,
        )

        assert_user_mechanisms(replica_set.get_automation_config_tester(), OM_NO_MECH_USER_NAME, [])
        replica_set.tester().assert_scram_sha_authentication(
            password=new_password,
            username=OM_NO_MECH_USER_NAME,
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
            tester.assert_expected_users(4)
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
            tester.assert_authentication_disabled(remaining_users=4)
            return True
        except AssertionError:
            return False

    wait_until(auth_disabled, timeout=600)
