from typing import Dict, Optional

import kubernetes
from kubetester import create_or_update_secret, find_fixture, read_secret, try_load, wait_until
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.mongotester import MongoTester
from kubetester.phase import Phase
from kubetester.scram import (
    assert_creds_preserved,
    assert_user_mechanisms,
    build_sha1_creds,
    build_sha256_creds,
    get_ac_user,
    seed_user_in_ac,
)
from pytest import fixture


class SHA1ConnectivityTests:
    # K8s-originated user (operator manages all creds).
    USER_NAME = "mms-user-1"
    USER_PASSWORD = "my-password"
    PASSWORD_SECRET_NAME = "mms-user-1-password"
    USER_DATABASE = "admin"

    NON_ADMIN_USER_NAME = "mms-user-2"
    NON_ADMIN_PASSWORD_SECRET_NAME = "mms-user-2-password"
    NON_ADMIN_USER_PASSWORD = "my-password-2"
    NON_ADMIN_USER_DATABASE = "testdb"

    # Imported user seeded in the AC with only SHA-1 creds + SHA-1 mechanism.
    OM_SHA1_USER_NAME = "om-user-sha1"
    OM_SHA1_USER_PASSWORD = "om-sha1-password-1"
    OM_SHA1_USER_PASSWORD_SECRET = "om-user-sha1-password"
    SEEDED_SHA1_CREDS = build_sha1_creds(OM_SHA1_USER_NAME, OM_SHA1_USER_PASSWORD)

    # Imported user seeded in the AC with both SHA-1 and SHA-256 creds + both mechanisms.
    OM_BOTH_USER_NAME = "om-user-both"
    OM_BOTH_USER_PASSWORD = "om-both-password-1"
    OM_BOTH_USER_PASSWORD_SECRET = "om-user-both-password"
    SEEDED_BOTH_SHA1_CREDS = build_sha1_creds(OM_BOTH_USER_NAME, OM_BOTH_USER_PASSWORD)
    SEEDED_BOTH_SHA256_CREDS = build_sha256_creds(OM_BOTH_USER_PASSWORD)

    # User seeded in the AC with null mechanisms and SHA-1 creds only.
    # Exercises the no-mechanisms path: SHA-1 preserved, SHA-256 generated.
    OM_NO_MECH_USER_NAME = "om-user-no-mech"
    OM_NO_MECH_USER_PASSWORD = "om-no-mech-password-1"
    OM_NO_MECH_USER_PASSWORD_SECRET = "om-user-no-mech-password"
    SEEDED_NO_MECH_SHA1_CREDS = build_sha1_creds(OM_NO_MECH_USER_NAME, OM_NO_MECH_USER_PASSWORD)

    # Captured from the AC after the operator's follow-up reconcile generates SHA-256
    # for the sha1-only user. Used to assert the creds do not change across the
    # subsequent SCRAM-SHA-256 mode upgrade.
    generated_sha1_user_sha256_creds: Optional[dict] = None

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

    def _seed_both_user_in_ac(self, mdb: MongoDB) -> None:
        seed_user_in_ac(
            om_tester=mdb.get_om_tester(),
            username=self.OM_BOTH_USER_NAME,
            db="admin",
            roles=[{"role": "readWrite", "db": "admin"}],
            mechanisms=["SCRAM-SHA-1", "SCRAM-SHA-256"],
            sha256_creds=self.SEEDED_BOTH_SHA256_CREDS,
            sha1_creds=self.SEEDED_BOTH_SHA1_CREDS,
        )

    def _build_both_user_in_k8s(self, namespace: str, mdb_resource_name: str) -> MongoDBUser:
        create_or_update_secret(namespace, self.OM_BOTH_USER_PASSWORD_SECRET, {"password": self.OM_BOTH_USER_PASSWORD})
        resource = MongoDBUser.from_yaml(
            find_fixture("scram-sha-user.yaml"), namespace=namespace, name=self.OM_BOTH_USER_NAME
        )
        resource["spec"]["username"] = self.OM_BOTH_USER_NAME
        resource["spec"]["passwordSecretKeyRef"] = {"name": self.OM_BOTH_USER_PASSWORD_SECRET, "key": "password"}
        resource["spec"]["mongodbResourceRef"]["name"] = mdb_resource_name
        try_load(resource)
        return resource

    def _seed_sha1_user_in_ac(self, mdb: MongoDB) -> None:
        seed_user_in_ac(
            om_tester=mdb.get_om_tester(),
            username=self.OM_SHA1_USER_NAME,
            db="admin",
            roles=[{"role": "readWrite", "db": "admin"}],
            mechanisms=["SCRAM-SHA-1"],
            sha1_creds=self.SEEDED_SHA1_CREDS,
        )

    def _build_sha1_user_in_k8s(self, namespace: str, mdb_resource_name: str) -> MongoDBUser:
        create_or_update_secret(namespace, self.OM_SHA1_USER_PASSWORD_SECRET, {"password": self.OM_SHA1_USER_PASSWORD})
        resource = MongoDBUser.from_yaml(
            find_fixture("scram-sha-user.yaml"), namespace=namespace, name=self.OM_SHA1_USER_NAME
        )
        resource["spec"]["username"] = self.OM_SHA1_USER_NAME
        resource["spec"]["passwordSecretKeyRef"] = {"name": self.OM_SHA1_USER_PASSWORD_SECRET, "key": "password"}
        resource["spec"]["mongodbResourceRef"]["name"] = mdb_resource_name
        try_load(resource)
        return resource

    def test_seed_sha1_user_in_ac(self, mdb: MongoDB):
        self._seed_sha1_user_in_ac(mdb)

    def test_om_user_sha1_created(self, namespace: str, mdb_resource_name: str):
        resource = self._build_sha1_user_in_k8s(namespace, mdb_resource_name)
        resource.update()
        resource.assert_reaches_phase(Phase.Updated)

    def test_om_user_sha1_mechanisms_empty_after_transition(self, mdb: MongoDB):
        # After initPwd is processed by OM and the follow-up reconcile completes,
        # the operator treats the user as K8s-managed (mechanisms=[]).
        tester = mdb.get_automation_config_tester()
        tester.assert_has_user(self.OM_SHA1_USER_NAME)
        assert_user_mechanisms(tester, self.OM_SHA1_USER_NAME, [])

    def test_om_user_sha1_creds_preserved_byte_for_byte(self, mdb: MongoDB):
        # The original SHA-1 creds seeded in the AC must survive the import transition
        # byte-for-byte. SHA-256 is generated separately on the follow-up reconcile and
        # must not affect SHA-1.
        assert_creds_preserved(
            mdb.get_automation_config_tester(),
            self.OM_SHA1_USER_NAME,
            sha1_creds=self.SEEDED_SHA1_CREDS,
        )

    def test_om_user_sha1_gets_sha256_creds_after_transition(self, mdb: MongoDB):
        # On the follow-up reconcile the operator treats the user as K8s-managed
        # (mechanisms=[]) and generates only the missing SHA-256 creds. We capture
        # them so we can assert they are not regenerated by the mode upgrade.
        user = get_ac_user(mdb.get_automation_config_tester(), self.OM_SHA1_USER_NAME)
        assert user.get("scramSha1Creds"), "SHA-1 creds must be present"
        assert user.get("scramSha256Creds"), "SHA-256 creds must be present after the follow-up reconcile"
        SHA1ConnectivityTests.generated_sha1_user_sha256_creds = user["scramSha256Creds"]

    def test_om_user_sha1_can_authenticate_after_transition(self, mdb: MongoDB):
        mdb.tester().assert_scram_sha_authentication(
            password=self.OM_SHA1_USER_PASSWORD,
            username=self.OM_SHA1_USER_NAME,
            auth_mechanism="SCRAM-SHA-1",
            attempts=20,
        )

    def test_add_scram_sha_256_mode(self, mdb: MongoDB):
        mdb.load()
        modes = mdb["spec"]["security"]["authentication"]["modes"]
        if "SCRAM-SHA-256" not in modes:
            mdb["spec"]["security"]["authentication"]["modes"] = ["SCRAM-SHA-256"] + modes
            mdb["spec"]["security"]["authentication"]["ignoreUnknownUsers"] = True
            mdb.update()
            mdb.assert_reaches_phase(Phase.Running)

    def test_om_user_sha1_creds_preserved_byte_for_byte_after_mode_upgrade(self, mdb: MongoDB):
        # Both creds were already present before the mode upgrade so neither should
        # change. SHA-1 is compared against the seeded value and SHA-256 against the
        # value captured right after the operator generated it.
        assert_creds_preserved(
            mdb.get_automation_config_tester(),
            self.OM_SHA1_USER_NAME,
            sha1_creds=self.SEEDED_SHA1_CREDS,
            sha256_creds=self.generated_sha1_user_sha256_creds,
        )

    def test_om_user_sha1_can_authenticate_sha256_after_mode_upgrade(self, mdb: MongoDB):
        mdb.tester().assert_scram_sha_authentication(
            password=self.OM_SHA1_USER_PASSWORD,
            username=self.OM_SHA1_USER_NAME,
            auth_mechanism="SCRAM-SHA-256",
            attempts=20,
        )

    def test_seed_both_user_in_ac(self, mdb: MongoDB):
        self._seed_both_user_in_ac(mdb)

    def test_om_user_both_created(self, namespace: str, mdb_resource_name: str):
        resource = self._build_both_user_in_k8s(namespace, mdb_resource_name)
        resource.update()
        resource.assert_reaches_phase(Phase.Updated)

    def test_om_user_both_mechanisms_empty_after_transition(self, mdb: MongoDB):
        # After initPwd is processed by OM and the follow-up reconcile completes,
        # the operator treats the user as K8s-managed (mechanisms=[]).
        tester = mdb.get_automation_config_tester()
        tester.assert_has_user(self.OM_BOTH_USER_NAME)
        assert_user_mechanisms(tester, self.OM_BOTH_USER_NAME, [])

    def test_om_user_both_creds_preserved_byte_for_byte(self, mdb: MongoDB):
        # Both SHA-256 and SHA-1 were seeded so OM has nothing to generate via
        # initPwd and leaves both sets of creds untouched.
        assert_creds_preserved(
            mdb.get_automation_config_tester(),
            self.OM_BOTH_USER_NAME,
            sha256_creds=self.SEEDED_BOTH_SHA256_CREDS,
            sha1_creds=self.SEEDED_BOTH_SHA1_CREDS,
        )

    def test_om_user_both_can_authenticate_after_transition(self, mdb: MongoDB):
        mdb.tester().assert_scram_sha_authentication(
            password=self.OM_BOTH_USER_PASSWORD,
            username=self.OM_BOTH_USER_NAME,
            auth_mechanism="SCRAM-SHA-1",
            attempts=20,
        )

    def _seed_no_mech_user_in_ac(self, mdb: MongoDB) -> None:
        seed_user_in_ac(
            om_tester=mdb.get_om_tester(),
            username=self.OM_NO_MECH_USER_NAME,
            db="admin",
            roles=[{"role": "readWrite", "db": "admin"}],
            mechanisms=None,
            sha1_creds=self.SEEDED_NO_MECH_SHA1_CREDS,
        )

    def _build_no_mech_user_in_k8s(self, namespace: str, mdb_resource_name: str) -> MongoDBUser:
        create_or_update_secret(namespace, self.OM_NO_MECH_USER_PASSWORD_SECRET, {"password": self.OM_NO_MECH_USER_PASSWORD})
        resource = MongoDBUser.from_yaml(
            find_fixture("scram-sha-user.yaml"), namespace=namespace, name=self.OM_NO_MECH_USER_NAME
        )
        resource["spec"]["username"] = self.OM_NO_MECH_USER_NAME
        resource["spec"]["passwordSecretKeyRef"] = {"name": self.OM_NO_MECH_USER_PASSWORD_SECRET, "key": "password"}
        resource["spec"]["mongodbResourceRef"]["name"] = mdb_resource_name
        try_load(resource)
        return resource

    def test_seed_no_mech_user_in_ac(self, mdb: MongoDB):
        self._seed_no_mech_user_in_ac(mdb)

    def test_om_user_no_mech_created(self, namespace: str, mdb_resource_name: str):
        resource = self._build_no_mech_user_in_k8s(namespace, mdb_resource_name)
        resource.update()
        resource.assert_reaches_phase(Phase.Updated)

    def test_null_mechanisms_user_treated_as_k8s_managed(self, mdb: MongoDB):
        tester = mdb.get_automation_config_tester()
        tester.assert_has_user(self.OM_NO_MECH_USER_NAME)
        assert_user_mechanisms(tester, self.OM_NO_MECH_USER_NAME, [])

    def test_null_mechanisms_user_sha1_creds_preserved(self, mdb: MongoDB):
        # The original SHA-1 creds seeded in the AC must survive byte-for-byte.
        # SHA-256 is generated on the same reconcile but must not touch SHA-1.
        assert_creds_preserved(
            mdb.get_automation_config_tester(),
            self.OM_NO_MECH_USER_NAME,
            sha1_creds=self.SEEDED_NO_MECH_SHA1_CREDS,
        )

    def test_null_mechanisms_user_gets_sha256_creds(self, mdb: MongoDB):
        user = get_ac_user(mdb.get_automation_config_tester(), self.OM_NO_MECH_USER_NAME)
        assert user.get("scramSha1Creds"), "SHA-1 creds must be present"
        assert user.get("scramSha256Creds"), "SHA-256 creds must be generated for a no-mechanisms user"

    def test_null_mechanisms_user_can_authenticate(self, mdb: MongoDB):
        mdb.tester().assert_scram_sha_authentication(
            password=self.OM_NO_MECH_USER_PASSWORD,
            username=self.OM_NO_MECH_USER_NAME,
            auth_mechanism="SCRAM-SHA-1",
            attempts=20,
        )

    def test_null_mechanisms_user_password_can_change(self, namespace: str, mdb: MongoDB):
        ac_version = mdb.get_automation_config_tester().automation_config["version"]
        new_password = "om-no-mech-password-new-1"
        KubernetesTester.update_secret(namespace, self.OM_NO_MECH_USER_PASSWORD_SECRET, {"password": new_password})

        wait_until(
            lambda: mdb.get_automation_config_tester().reached_version(ac_version + 1),
            timeout=600,
        )

        assert_user_mechanisms(mdb.get_automation_config_tester(), self.OM_NO_MECH_USER_NAME, [])
        mdb.tester().assert_scram_sha_authentication(
            password=new_password,
            username=self.OM_NO_MECH_USER_NAME,
            auth_mechanism="SCRAM-SHA-1",
        )

    # Credentials secret connectivity

    @fixture
    def standard_secret(self, namespace: str, mdb_resource_name: str) -> Dict[str, str]:
        secret_name = f"{mdb_resource_name}-{self.USER_NAME}-{self.USER_DATABASE}"
        return read_secret(namespace, secret_name)

    @fixture
    def non_admin_standard_secret(self, namespace: str, mdb_resource_name: str) -> Dict[str, str]:
        secret_name = f"{mdb_resource_name}-{self.NON_ADMIN_USER_NAME}-{self.NON_ADMIN_USER_DATABASE}"
        return read_secret(namespace, secret_name)

    def test_credentials_secret_is_created(self, standard_secret: Dict[str, str]):
        assert "username" in standard_secret
        assert "password" in standard_secret
        assert "connectionString.standard" in standard_secret
        assert "connectionString.standardSrv" in standard_secret
        assert f"authSource={self.USER_DATABASE}" in standard_secret["connectionString.standard"]
        assert f"authSource={self.USER_DATABASE}" in standard_secret["connectionString.standardSrv"]

    def test_credentials_can_connect_to_db(self, standard_secret: Dict[str, str]):
        MongoTester(standard_secret["connectionString.standard"], use_ssl=False).assert_connectivity()

    def test_credentials_can_connect_to_db_with_srv(self, standard_secret: Dict[str, str]):
        MongoTester(standard_secret["connectionString.standardSrv"], use_ssl=False).assert_connectivity()

    def test_create_non_admin_db_user(self, namespace: str, mdb_resource_name: str):
        create_or_update_secret(
            namespace, self.NON_ADMIN_PASSWORD_SECRET_NAME, {"password": self.NON_ADMIN_USER_PASSWORD}
        )
        resource = MongoDBUser.from_yaml(yaml_fixture("scram-sha-user-non-admin-db.yaml"), namespace=namespace)
        resource["spec"]["mongodbResourceRef"]["name"] = mdb_resource_name
        try_load(resource)
        resource.update()
        resource.assert_reaches_phase(Phase.Updated, timeout=150)

    def test_non_admin_db_credentials_secret_is_created(self, non_admin_standard_secret: Dict[str, str]):
        assert "username" in non_admin_standard_secret
        assert "password" in non_admin_standard_secret
        assert "connectionString.standard" in non_admin_standard_secret
        assert "connectionString.standardSrv" in non_admin_standard_secret
        assert f"authSource={self.NON_ADMIN_USER_DATABASE}" in non_admin_standard_secret["connectionString.standard"]
        assert f"authSource={self.NON_ADMIN_USER_DATABASE}" in non_admin_standard_secret["connectionString.standardSrv"]

    def test_non_admin_credentials_can_connect_to_db(self, non_admin_standard_secret: Dict[str, str]):
        MongoTester(non_admin_standard_secret["connectionString.standard"], use_ssl=False).assert_connectivity()

    def test_non_admin_credentials_can_connect_to_db_with_srv(self, non_admin_standard_secret: Dict[str, str]):
        MongoTester(non_admin_standard_secret["connectionString.standardSrv"], use_ssl=False).assert_connectivity()

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
