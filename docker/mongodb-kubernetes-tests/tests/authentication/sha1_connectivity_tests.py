import kubernetes
from kubetester import create_or_update_secret, find_fixture, try_load, update_secret, wait_until
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.mongotester import MongoTester
from kubetester.phase import Phase
from pytest import fixture


def _get_ac_user(ac_tester, username: str) -> dict:
    users = ac_tester.automation_config["auth"]["usersWanted"]
    matches = [u for u in users if u["user"] == username]
    assert matches, f"User {username!r} not found in usersWanted"
    return matches[0]


def _assert_user_mechanisms(ac_tester, username: str, expected: list) -> None:
    user = _get_ac_user(ac_tester, username)
    assert user.get("mechanisms", []) == expected, (
        f"User {username!r} mechanisms: expected {expected}, got {user.get('mechanisms', [])}"
    )


class SHA1ConnectivityTests:
    OM_BOTH_USER_NAME = "om-user-both"
    OM_BOTH_USER_PASSWORD_SECRET = "om-user-both-password"
    OM_BOTH_USER_PASSWORD = "om-both-password-1"
    OM_SHA1_USER_NAME = "om-user-sha1"
    OM_SHA1_USER_PASSWORD_SECRET = "om-user-sha1-password"
    OM_SHA1_USER_PASSWORD = "om-sha1-password-1"
    PASSWORD_SECRET_NAME = "mms-user-1-password"
    USER_PASSWORD = "my-password"
    USER_NAME = "mms-user-1"

    @fixture
    def yaml_file(self):
        raise Exception("Not implemented, should be defined in a subclass")

    @fixture
    def mdb_resource_name(self):
        raise Exception("Not implemented, should be defined in a subclass")

    @fixture
    def mongo_tester(self, mdb_resource_name: str):
        raise Exception("Not implemented, should be defined in a subclass")

    @fixture
    def mdb(self, namespace, mdb_resource_name, yaml_file, custom_mdb_version: str):
        mdb = MongoDB.from_yaml(
            yaml_fixture(yaml_file),
            namespace=namespace,
            name=mdb_resource_name,
        )
        mdb["spec"]["version"] = custom_mdb_version

        try_load(mdb)
        return mdb

    def test_create_cluster(self, mdb: MongoDB):
        mdb.update()
        mdb.assert_reaches_phase(Phase.Running)

    def test_cluster_connectivity(self, mongo_tester: MongoTester):
        mongo_tester.assert_connectivity()

    def test_ops_manager_state_correctly_updated(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_enabled("MONGODB-CR")
        tester.assert_authoritative_set(True)
        tester.assert_authentication_enabled(2)
        tester.assert_expected_users(0)

    # CreateMongoDBUser

    def test_create_secret(self):
        print(f"creating password for MongoDBUser {self.USER_NAME} in secret/{self.PASSWORD_SECRET_NAME} ")

        create_or_update_secret(
            KubernetesTester.get_namespace(),
            self.PASSWORD_SECRET_NAME,
            {
                "password": self.USER_PASSWORD,
            },
        )

    def test_create_user(self, namespace: str, mdb_resource_name: str):
        mdb = MongoDBUser.from_yaml(
            yaml_fixture("scram-sha-user.yaml"),
            namespace=namespace,
        )
        mdb["spec"]["mongodbResourceRef"]["name"] = mdb_resource_name

        mdb.update()
        mdb.assert_reaches_phase(Phase.Updated, timeout=150)

    # ClusterIsUpdatedWithNewUser

    def test_ops_manager_state_with_users_correctly_updated(self):
        expected_roles = {
            ("admin", "clusterAdmin"),
            ("admin", "userAdminAnyDatabase"),
            ("admin", "readWrite"),
            ("admin", "userAdminAnyDatabase"),
        }

        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_has_user(self.USER_NAME)
        tester.assert_user_has_roles(self.USER_NAME, expected_roles)
        tester.assert_expected_users(1)

    def test_user_cannot_authenticate_with_incorrect_password(self, mongo_tester: MongoTester):
        mongo_tester.assert_scram_sha_authentication_fails(
            password="invalid-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-1",
        )

    def test_user_can_authenticate_with_correct_password(self, mongo_tester: MongoTester):
        mongo_tester.assert_scram_sha_authentication(
            password="my-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-1",
            attempts=20,
        )

    # CanChangePassword

    def test_update_secret(self, mdb: MongoDB):
        print(f"updating password for MongoDBUser {self.USER_NAME} in secret/{self.PASSWORD_SECRET_NAME}")
        KubernetesTester.update_secret(
            KubernetesTester.get_namespace(),
            self.PASSWORD_SECRET_NAME,
            {"password": "my-new-password"},
        )

    def test_user_can_authenticate_with_new_password(self, mongo_tester: MongoTester):
        mongo_tester.assert_scram_sha_authentication(
            password="my-new-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-1",
            attempts=20,
        )

    def test_user_cannot_authenticate_with_old_password(self, mongo_tester: MongoTester):
        mongo_tester.assert_scram_sha_authentication_fails(
            password="my-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-1",
        )

    def test_add_scram_sha_256_mode(self, mdb: MongoDB):
        """Upgrade the replica set to include SCRAM-SHA-256 before testing OM-originated user scenarios."""
        mdb.load()
        modes = mdb["spec"]["security"]["authentication"]["modes"]
        if "SCRAM-SHA-256" not in modes:
            mdb["spec"]["security"]["authentication"]["modes"] = ["SCRAM-SHA-256"] + modes
            mdb["spec"]["security"]["authentication"]["ignoreUnknownUsers"] = True
            mdb.update()
            mdb.assert_reaches_phase(Phase.Running)

    @fixture
    def om_user_both(self, namespace: str, mdb: MongoDB, mdb_resource_name: str) -> MongoDBUser:
        create_or_update_secret(namespace, self.OM_BOTH_USER_PASSWORD_SECRET, {"password": self.OM_BOTH_USER_PASSWORD})
        mdb.get_om_tester().add_user(
            username=self.OM_BOTH_USER_NAME,
            database="admin",
            password=self.OM_BOTH_USER_PASSWORD,
            mechanisms=["SCRAM-SHA-1", "SCRAM-SHA-256"],
            roles=[{"role": "readWrite", "db": "admin"}],
        )
        resource = MongoDBUser.from_yaml(find_fixture("scram-sha-user.yaml"), namespace=namespace, name=self.OM_BOTH_USER_NAME)
        resource["spec"]["username"] = self.OM_BOTH_USER_NAME
        resource["spec"]["passwordSecretKeyRef"] = {"name": self.OM_BOTH_USER_PASSWORD_SECRET, "key": "password"}
        resource["spec"]["mongodbResourceRef"]["name"] = mdb_resource_name
        try_load(resource)
        return resource

    @fixture
    def om_user_sha1(self, namespace: str, mdb: MongoDB, mdb_resource_name: str) -> MongoDBUser:
        create_or_update_secret(namespace, self.OM_SHA1_USER_PASSWORD_SECRET, {"password": self.OM_SHA1_USER_PASSWORD})
        mdb.get_om_tester().add_user(
            username=self.OM_SHA1_USER_NAME,
            database="admin",
            password=self.OM_SHA1_USER_PASSWORD,
            mechanisms=["SCRAM-SHA-1"],
            roles=[{"role": "readWrite", "db": "admin"}],
        )
        resource = MongoDBUser.from_yaml(find_fixture("scram-sha-user.yaml"), namespace=namespace, name=self.OM_SHA1_USER_NAME)
        resource["spec"]["username"] = self.OM_SHA1_USER_NAME
        resource["spec"]["passwordSecretKeyRef"] = {"name": self.OM_SHA1_USER_PASSWORD_SECRET, "key": "password"}
        resource["spec"]["mongodbResourceRef"]["name"] = mdb_resource_name
        try_load(resource)
        return resource

    def test_om_user_both_created(self, om_user_both: MongoDBUser):
        om_user_both.update()
        om_user_both.assert_reaches_phase(Phase.Updated)

    def test_om_user_both_mechanisms_in_ac(self, mdb: MongoDB):
        tester = mdb.get_automation_config_tester()
        tester.assert_has_user(self.OM_BOTH_USER_NAME)
        user = _get_ac_user(tester, self.OM_BOTH_USER_NAME)
        mechanisms = user.get("mechanisms", [])
        assert "SCRAM-SHA-256" in mechanisms and "SCRAM-SHA-1" in mechanisms, (
            f"Expected both mechanisms, got {mechanisms}"
        )

    def test_om_user_both_password_change_preserves_mechanisms(self, namespace: str, mdb: MongoDB):
        ac_version = mdb.get_automation_config_tester().automation_config["version"]
        new_password = "om-both-password-new-1"
        update_secret(namespace, self.OM_BOTH_USER_PASSWORD_SECRET, {"password": new_password})
        wait_until(
            lambda: mdb.get_automation_config_tester().reached_version(ac_version + 1),
            timeout=600,
        )
        user = _get_ac_user(mdb.get_automation_config_tester(), self.OM_BOTH_USER_NAME)
        assert len(user.get("mechanisms", [])) == 2, "Both mechanisms should be preserved after password change"
        mdb.tester().assert_scram_sha_authentication(
            password=new_password,
            username=self.OM_BOTH_USER_NAME,
            auth_mechanism="SCRAM-SHA-1",
        )

    def test_om_user_sha1_created(self, om_user_sha1: MongoDBUser):
        om_user_sha1.update()
        om_user_sha1.assert_reaches_phase(Phase.Updated)

    def test_om_user_sha1_only_mechanism_in_ac(self, mdb: MongoDB):
        tester = mdb.get_automation_config_tester()
        tester.assert_has_user(self.OM_SHA1_USER_NAME)
        _assert_user_mechanisms(tester, self.OM_SHA1_USER_NAME, ["SCRAM-SHA-1"])

    def test_om_user_sha1_has_no_sha256_creds(self, mdb: MongoDB):
        user = _get_ac_user(mdb.get_automation_config_tester(), self.OM_SHA1_USER_NAME)
        assert user.get("scramSha1Creds"), "SHA-1 creds must be present"
        assert not user.get("scramSha256Creds"), "SHA-256 creds must NOT be present"

    def test_om_user_sha1_password_change_preserves_mechanism(self, namespace: str, mdb: MongoDB):
        ac_version = mdb.get_automation_config_tester().automation_config["version"]
        new_password = "om-sha1-password-new-1"
        update_secret(namespace, self.OM_SHA1_USER_PASSWORD_SECRET, {"password": new_password})
        wait_until(
            lambda: mdb.get_automation_config_tester().reached_version(ac_version + 1),
            timeout=600,
        )
        tester = mdb.get_automation_config_tester()
        _assert_user_mechanisms(tester, self.OM_SHA1_USER_NAME, ["SCRAM-SHA-1"])
        assert not _get_ac_user(tester, self.OM_SHA1_USER_NAME).get("scramSha256Creds"), (
            "SHA-256 creds must NOT appear after password change"
        )

    def test_authentication_is_disabled_once_resource_is_deleted(self, mdb: MongoDB):
        mdb.delete()

        def resource_is_deleted() -> bool:
            try:
                mdb.load()
                return False
            except kubernetes.client.ApiException as e:
                return e.status == 404

        # wait until the resource is deleted
        run_periodically(resource_is_deleted, timeout=300)

        def authentication_was_disabled() -> bool:
            return KubernetesTester.get_automation_config()["auth"]["disabled"]

        run_periodically(authentication_was_disabled, timeout=60)
