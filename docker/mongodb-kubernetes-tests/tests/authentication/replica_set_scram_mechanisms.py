"""
E2E tests for SCRAM mechanisms preservation behaviour.

Scenarios covered:
  1. K8s-created user        → mechanisms=[] in AC, both SHA-256 and SHA-1 creds present
  2. SCRAM disabled mid-way  → user reconciliation retries and recovers when re-enabled
                               (must run before OM users are injected — disabling auth sets
                               deploymentAuthMechanisms=[], which OM rejects if any user
                               has an explicit mechanism set)
  3. OM user with SHA-256    → mechanisms=[SCRAM-SHA-256] only, password change preserves

Note: OM users with SHA-1 mechanisms are tested in replica_set_scram_sha_1_mechanisms.py
      because they require a replica set with SCRAM-SHA-1/MONGODB-CR modes enabled.
"""

from typing import List

from kubetester import create_or_update_secret, find_fixture, try_load, update_secret, wait_until
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark

MDB_RESOURCE = "my-replica-set"

# K8s-created user
K8S_USER_NAME = "k8s-user"
K8S_USER_PASSWORD_SECRET = "k8s-user-password"
K8S_USER_PASSWORD = "k8s-password-1"

# OM-originated user — SHA-256 only
OM_SHA256_USER_NAME = "om-user-sha256"
OM_SHA256_USER_PASSWORD_SECRET = "om-user-sha256-password"
OM_SHA256_USER_PASSWORD = "om-sha256-password-1"


SCRAM_SHA_256 = "SCRAM-SHA-256"


def _get_ac_user(ac_tester, username: str) -> dict:
    """Return the raw automation config user entry for the given username."""
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
        "modes": ["SCRAM"],
    }
    try_load(resource)
    return resource


# ---------------------------------------------------------------------------
# Scenario 1: K8s-created user — mechanisms should be [] in AC
# ---------------------------------------------------------------------------


@fixture(scope="module")
def k8s_user(namespace: str, replica_set: MongoDB) -> MongoDBUser:
    create_or_update_secret(namespace, K8S_USER_PASSWORD_SECRET, {"password": K8S_USER_PASSWORD})
    resource = MongoDBUser.from_yaml(find_fixture("scram-sha-user.yaml"), namespace=namespace, name=K8S_USER_NAME)
    resource["spec"]["username"] = K8S_USER_NAME
    resource["spec"]["passwordSecretKeyRef"] = {"name": K8S_USER_PASSWORD_SECRET, "key": "password"}
    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE
    try_load(resource)
    return resource


@mark.e2e_replica_set_scram_mechanisms
def test_install_operator(default_operator: Operator):
    default_operator.assert_is_running()


@mark.e2e_replica_set_scram_mechanisms
def test_replica_set_running(replica_set: MongoDB):
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_scram_mechanisms
def test_k8s_user_created(k8s_user: MongoDBUser):
    k8s_user.update()
    k8s_user.assert_reaches_phase(Phase.Updated, timeout=600)


@mark.e2e_replica_set_scram_mechanisms
class TestK8sUserHasEmptyMechanisms(KubernetesTester):
    def test_k8s_user_mechanisms_empty_in_ac(self, replica_set: MongoDB):
        """K8s-originated user must have mechanisms=[] regardless of which creds are present."""
        tester = replica_set.get_automation_config_tester()
        tester.assert_has_user(K8S_USER_NAME)
        _assert_user_mechanisms(tester, K8S_USER_NAME, [])

    def test_k8s_user_has_both_creds(self, replica_set: MongoDB):
        """Both SHA-256 and SHA-1 creds must exist even though mechanisms is []."""
        tester = replica_set.get_automation_config_tester()
        user = _get_ac_user(tester, K8S_USER_NAME)
        assert user.get("scramSha256Creds"), "scramSha256Creds should be present"
        assert user.get("scramSha1Creds"), "scramSha1Creds should be present"

    def test_k8s_user_can_authenticate(self, replica_set: MongoDB):
        replica_set.tester().assert_scram_sha_authentication(
            password=K8S_USER_PASSWORD,
            username=K8S_USER_NAME,
            auth_mechanism=SCRAM_SHA_256,
        )


@mark.e2e_replica_set_scram_mechanisms
class TestK8sUserPasswordChangeKeepsEmptyMechanisms(KubernetesTester):
    def test_password_change_preserves_empty_mechanisms(self, namespace: str, replica_set: MongoDB):
        ac_version = replica_set.get_automation_config_tester().automation_config["version"]
        new_password = "k8s-password-new-1"
        update_secret(namespace, K8S_USER_PASSWORD_SECRET, {"password": new_password})

        wait_until(
            lambda: replica_set.get_automation_config_tester().reached_version(ac_version + 1),
            timeout=600,
        )

        tester = replica_set.get_automation_config_tester()
        _assert_user_mechanisms(tester, K8S_USER_NAME, [])

        replica_set.tester().assert_scram_sha_authentication(
            password=new_password,
            username=K8S_USER_NAME,
            auth_mechanism=SCRAM_SHA_256,
        )


# ---------------------------------------------------------------------------
# Scenario 2: SCRAM disabled then re-enabled — user recovers to Updated
# (runs before OM users are injected to avoid deploymentAuthMechanisms conflict)
# ---------------------------------------------------------------------------


@mark.e2e_replica_set_scram_mechanisms
class TestScramDisabledAndReenabled(KubernetesTester):
    def test_disable_scram(self, replica_set: MongoDB):
        replica_set["spec"]["security"]["authentication"] = {"enabled": False}
        replica_set.update()
        replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    def test_k8s_user_pending_while_scram_disabled(self, k8s_user: MongoDBUser):
        """User should move to Pending while SCRAM is disabled."""
        k8s_user.load()
        assert k8s_user.get_status_phase() in (Phase.Updated, Phase.Pending)

    def test_reenable_scram(self, replica_set: MongoDB):
        replica_set["spec"]["security"]["authentication"] = {
            "ignoreUnknownUsers": True,
            "enabled": True,
            "modes": ["SCRAM"],
        }
        replica_set.update()
        replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    def test_k8s_user_recovers_to_updated(self, k8s_user: MongoDBUser):
        k8s_user.assert_reaches_phase(Phase.Updated, timeout=300)

    def test_k8s_user_mechanisms_still_empty_after_recovery(self, replica_set: MongoDB):
        tester = replica_set.get_automation_config_tester()
        _assert_user_mechanisms(tester, K8S_USER_NAME, [])


# ---------------------------------------------------------------------------
# Scenario 3: OM user with SHA-256 only — preserved after password change
# ---------------------------------------------------------------------------


@fixture(scope="module")
def om_user_sha256(namespace: str, replica_set: MongoDB) -> MongoDBUser:
    create_or_update_secret(namespace, OM_SHA256_USER_PASSWORD_SECRET, {"password": OM_SHA256_USER_PASSWORD})

    replica_set.get_om_tester().add_user(
        username=OM_SHA256_USER_NAME,
        database="admin",
        password=OM_SHA256_USER_PASSWORD,
        mechanisms=[SCRAM_SHA_256],
        roles=[{"role": "readWrite", "db": "admin"}],
    )

    resource = MongoDBUser.from_yaml(find_fixture("scram-sha-user.yaml"), namespace=namespace, name=OM_SHA256_USER_NAME)
    resource["spec"]["username"] = OM_SHA256_USER_NAME
    resource["spec"]["passwordSecretKeyRef"] = {"name": OM_SHA256_USER_PASSWORD_SECRET, "key": "password"}
    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE
    try_load(resource)
    return resource


@mark.e2e_replica_set_scram_mechanisms
def test_om_user_sha256_created(om_user_sha256: MongoDBUser):
    om_user_sha256.update()
    om_user_sha256.assert_reaches_phase(Phase.Updated)


@mark.e2e_replica_set_scram_mechanisms
class TestOMUserSha256OnlyPreserved(KubernetesTester):
    def test_om_user_sha256_only_mechanism_in_ac(self, replica_set: MongoDB):
        tester = replica_set.get_automation_config_tester()
        tester.assert_has_user(OM_SHA256_USER_NAME)
        _assert_user_mechanisms(tester, OM_SHA256_USER_NAME, [SCRAM_SHA_256])

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
        _assert_user_mechanisms(tester, OM_SHA256_USER_NAME, [SCRAM_SHA_256])
        user = _get_ac_user(tester, OM_SHA256_USER_NAME)
        assert not user.get("scramSha1Creds"), "SHA-1 creds must NOT appear after password change"

        replica_set.tester().assert_scram_sha_authentication(
            password=new_password,
            username=OM_SHA256_USER_NAME,
            auth_mechanism=SCRAM_SHA_256,
        )
