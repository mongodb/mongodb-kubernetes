"""
E2E tests for SCRAM-SHA-1 mechanism preservation for OM-originated users.

This test requires a replica set with SCRAM-SHA-1/MONGODB-CR modes enabled,
which is why it lives in a separate file from replica_set_scram_mechanisms.py.

Scenarios covered:
  1. OM user with both mechanisms → mechanisms=[SCRAM-SHA-1, SCRAM-SHA-256], password change preserves both
  2. OM user with SHA-1 only     → mechanisms=[SCRAM-SHA-1], password change preserves
"""

from typing import List

from kubetester import create_or_update_secret, find_fixture, try_load, update_secret, wait_until
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark

MDB_RESOURCE = "my-replica-set-sha1"

# OM-originated user — both mechanisms
OM_BOTH_USER_NAME = "om-user-both"
OM_BOTH_USER_PASSWORD_SECRET = "om-user-both-password"
OM_BOTH_USER_PASSWORD = "om-both-password-1"

# OM-originated user — SHA-1 only
OM_SHA1_USER_NAME = "om-user-sha1"
OM_SHA1_USER_PASSWORD_SECRET = "om-user-sha1-password"
OM_SHA1_USER_PASSWORD = "om-sha1-password-1"

SCRAM_SHA_1 = "SCRAM-SHA-1"
SCRAM_SHA_256 = "SCRAM-SHA-256"


def _get_ac_user(ac_tester, username: str) -> dict:
    users = ac_tester.automation_config["auth"]["usersWanted"]
    matches = [u for u in users if u["user"] == username]
    assert matches, f"User {username!r} not found in usersWanted"
    return matches[0]


def _assert_user_mechanisms(ac_tester, username: str, expected: List[str]) -> None:
    user = _get_ac_user(ac_tester, username)
    assert (
        user.get("mechanisms", []) == expected
    ), f"User {username!r} mechanisms: expected {expected}, got {user.get('mechanisms', [])}"


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("replica-set-scram-sha-256.yaml"),
        namespace=namespace,
        name=MDB_RESOURCE,
    )
    resource.set_version(custom_mdb_version)
    resource["spec"]["security"]["authentication"] = {
        "ignoreUnknownUsers": True,
        "enabled": True,
        "modes": ["SCRAM-SHA-256", "SCRAM-SHA-1", "MONGODB-CR"],
        "agents": {"mode": "MONGODB-CR"},
    }
    try_load(resource)
    return resource


@fixture(scope="module")
def om_user_sha1(namespace: str, replica_set: MongoDB) -> MongoDBUser:
    create_or_update_secret(namespace, OM_SHA1_USER_PASSWORD_SECRET, {"password": OM_SHA1_USER_PASSWORD})

    replica_set.get_om_tester().add_user(
        username=OM_SHA1_USER_NAME,
        database="admin",
        password=OM_SHA1_USER_PASSWORD,
        mechanisms=[SCRAM_SHA_1],
        roles=[{"role": "readWrite", "db": "admin"}],
    )

    resource = MongoDBUser.from_yaml(find_fixture("scram-sha-user.yaml"), namespace=namespace, name=OM_SHA1_USER_NAME)
    resource["spec"]["username"] = OM_SHA1_USER_NAME
    resource["spec"]["passwordSecretKeyRef"] = {"name": OM_SHA1_USER_PASSWORD_SECRET, "key": "password"}
    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE
    try_load(resource)
    return resource


@fixture(scope="module")
def om_user_both(namespace: str, replica_set: MongoDB) -> MongoDBUser:
    """
    Simulate an OM-originated user by injecting an AC user with both
    mechanisms set before creating the MongoDBUser CR.
    """
    create_or_update_secret(namespace, OM_BOTH_USER_PASSWORD_SECRET, {"password": OM_BOTH_USER_PASSWORD})

    replica_set.get_om_tester().add_user(
        username=OM_BOTH_USER_NAME,
        database="admin",
        password=OM_BOTH_USER_PASSWORD,
        mechanisms=[SCRAM_SHA_1, SCRAM_SHA_256],
        roles=[{"role": "readWrite", "db": "admin"}],
    )

    resource = MongoDBUser.from_yaml(find_fixture("scram-sha-user.yaml"), namespace=namespace, name=OM_BOTH_USER_NAME)
    resource["spec"]["username"] = OM_BOTH_USER_NAME
    resource["spec"]["passwordSecretKeyRef"] = {"name": OM_BOTH_USER_PASSWORD_SECRET, "key": "password"}
    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE
    try_load(resource)
    return resource


@mark.e2e_replica_set_scram_sha_1_mechanisms
def test_install_operator(default_operator: Operator):
    default_operator.assert_is_running()


@mark.e2e_replica_set_scram_sha_1_mechanisms
def test_replica_set_running(replica_set: MongoDB):
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_scram_sha_1_mechanisms
def test_om_user_both_created(om_user_both: MongoDBUser):
    om_user_both.update()
    om_user_both.assert_reaches_phase(Phase.Updated)


@mark.e2e_replica_set_scram_sha_1_mechanisms
class TestOMUserBothMechanismsPreserved(KubernetesTester):
    def test_om_user_both_mechanisms_in_ac(self, replica_set: MongoDB):
        tester = replica_set.get_automation_config_tester()
        tester.assert_has_user(OM_BOTH_USER_NAME)
        user = _get_ac_user(tester, OM_BOTH_USER_NAME)
        mechanisms = user.get("mechanisms", [])
        assert SCRAM_SHA_256 in mechanisms and SCRAM_SHA_1 in mechanisms, f"Expected both mechanisms, got {mechanisms}"

    def test_om_user_both_password_change_preserves_mechanisms(self, namespace: str, replica_set: MongoDB):
        ac_version = replica_set.get_automation_config_tester().automation_config["version"]
        new_password = "om-both-password-new-1"
        update_secret(namespace, OM_BOTH_USER_PASSWORD_SECRET, {"password": new_password})

        wait_until(
            lambda: replica_set.get_automation_config_tester().reached_version(ac_version + 1),
            timeout=600,
        )

        tester = replica_set.get_automation_config_tester()
        user = _get_ac_user(tester, OM_BOTH_USER_NAME)
        assert len(user.get("mechanisms", [])) == 2, "Both mechanisms should be preserved after password change"

        replica_set.tester().assert_scram_sha_authentication(
            password=new_password,
            username=OM_BOTH_USER_NAME,
            auth_mechanism=SCRAM_SHA_1,
        )


@mark.e2e_replica_set_scram_sha_1_mechanisms
def test_om_user_sha1_created(om_user_sha1: MongoDBUser):
    om_user_sha1.update()
    om_user_sha1.assert_reaches_phase(Phase.Updated)


@mark.e2e_replica_set_scram_sha_1_mechanisms
class TestOMUserSha1OnlyPreserved(KubernetesTester):
    def test_om_user_sha1_only_mechanism_in_ac(self, replica_set: MongoDB):
        tester = replica_set.get_automation_config_tester()
        tester.assert_has_user(OM_SHA1_USER_NAME)
        _assert_user_mechanisms(tester, OM_SHA1_USER_NAME, [SCRAM_SHA_1])

    def test_om_user_sha1_has_no_sha256_creds(self, replica_set: MongoDB):
        user = _get_ac_user(replica_set.get_automation_config_tester(), OM_SHA1_USER_NAME)
        assert user.get("scramSha1Creds"), "SHA-1 creds must be present"
        assert not user.get("scramSha256Creds"), "SHA-256 creds must NOT be present"

    def test_om_user_sha1_password_change_preserves_mechanism(self, namespace: str, replica_set: MongoDB):
        ac_version = replica_set.get_automation_config_tester().automation_config["version"]
        new_password = "om-sha1-password-new-1"
        update_secret(namespace, OM_SHA1_USER_PASSWORD_SECRET, {"password": new_password})

        wait_until(
            lambda: replica_set.get_automation_config_tester().reached_version(ac_version + 1),
            timeout=600,
        )

        tester = replica_set.get_automation_config_tester()
        user = _get_ac_user(tester, OM_SHA1_USER_NAME)
        mechanisms = user.get("mechanisms", [])
        assert mechanisms == [
            SCRAM_SHA_1
        ], f"SHA-1 mechanism should be preserved after password change, got {mechanisms}"
        assert not user.get("scramSha256Creds"), "SHA-256 creds must NOT appear after password change"
