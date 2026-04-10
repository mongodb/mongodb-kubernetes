"""
E2E tests for SCRAM-SHA-1 mechanism preservation for OM-originated users.

This test requires a replica set with SCRAM-SHA-1/MONGODB-CR modes enabled,
which is why it lives in a separate file from replica_set_scram_mechanisms.py.

Scenarios covered:
  1. OM user with SHA-1 only → mechanisms=[SCRAM-SHA-1], password change preserves
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

# OM-originated user — SHA-1 only
OM_SHA1_USER_NAME = "om-user-sha1"
OM_SHA1_USER_PASSWORD_SECRET = "om-user-sha1-password"
OM_SHA1_USER_PASSWORD = "om-sha1-password-1"

SCRAM_SHA_1 = "SCRAM-SHA-1"


def _get_ac_user(ac_tester, username: str) -> dict:
    users = ac_tester.automation_config["auth"]["usersWanted"]
    matches = [u for u in users if u["user"] == username]
    assert matches, f"User {username!r} not found in usersWanted"
    return matches[0]


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
        "modes": ["SCRAM-SHA-1", "MONGODB-CR"],
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


@mark.e2e_replica_set_scram_sha_1_mechanisms
def test_install_operator(default_operator: Operator):
    default_operator.assert_is_running()


@mark.e2e_replica_set_scram_sha_1_mechanisms
def test_replica_set_running(replica_set: MongoDB):
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_scram_sha_1_mechanisms
def test_om_user_sha1_created(om_user_sha1: MongoDBUser):
    om_user_sha1.update()
    om_user_sha1.assert_reaches_phase(Phase.Updated)


@mark.e2e_replica_set_scram_sha_1_mechanisms
class TestOMUserSha1OnlyPreserved(KubernetesTester):
    def test_om_user_sha1_only_mechanism_in_ac(self, replica_set: MongoDB):
        tester = replica_set.get_automation_config_tester()
        tester.assert_has_user(OM_SHA1_USER_NAME)
        user = _get_ac_user(tester, OM_SHA1_USER_NAME)
        mechanisms = user.get("mechanisms", [])
        assert mechanisms == [SCRAM_SHA_1], f"Expected SHA-1 only mechanism, got {mechanisms}"

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
