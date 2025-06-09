import kubernetes
from kubetester import create_or_update_secret, try_load
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.mongotester import MongoTester
from kubetester.phase import Phase
from pytest import fixture


class SHA1ConnectivityTests:
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

    def test_authentication_is_disabled_once_resource_is_deleted(namespace: str, mdb: MongoDB):
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
