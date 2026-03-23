from typing import Dict

import pytest
from kubetester import create_or_update_secret, find_fixture, read_secret, try_load
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.mongotester import ShardedClusterTester
from kubetester.phase import Phase
from pytest import fixture

MDB_RESOURCE = "sharded-cluster-scram-sha-256"
USER_NAME = "mms-user-1"
PASSWORD_SECRET_NAME = "mms-user-1-password"
USER_PASSWORD = "my-password"
USER_DATABASE = "admin"

NON_ADMIN_USER_NAME = "mms-user-2"
NON_ADMIN_PASSWORD_SECRET_NAME = "mms-user-2-password"
NON_ADMIN_USER_PASSWORD = "my-password-2"
NON_ADMIN_USER_DATABASE = "testdb"


@fixture(scope="function")
def standard_secret(namespace: str):
    secret_name = "{}-{}-{}".format(MDB_RESOURCE, USER_NAME, USER_DATABASE)
    return read_secret(namespace, secret_name)


@fixture(scope="function")
def non_admin_standard_secret(namespace: str):
    secret_name = "{}-{}-{}".format(MDB_RESOURCE, NON_ADMIN_USER_NAME, NON_ADMIN_USER_DATABASE)
    return read_secret(namespace, secret_name)


@pytest.mark.e2e_sharded_cluster_scram_sha_256_user_connectivity
class TestShardedClusterCreation(KubernetesTester):
    """
    description: |
      Creates a Sharded Cluster and checks everything is created as expected.
    """

    def test_create_sharded_cluster(self, custom_mdb_version: str):
        resource = MongoDB.from_yaml(load_fixture("sharded-cluster-scram-sha-256.yaml"), namespace=self.namespace)
        resource.set_version(custom_mdb_version)
        resource.update()

        resource.assert_reaches_phase(Phase.Running)

    def test_sharded_cluster_connectivity(self):
        ShardedClusterTester(MDB_RESOURCE, 2).assert_connectivity()

    def test_ops_manager_state_correctly_updated(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
        tester.assert_authentication_enabled()


@pytest.mark.e2e_sharded_cluster_scram_sha_256_user_connectivity
class TestCreateMongoDBUser(KubernetesTester):
    """
    description: |
      Creates a MongoDBUser
    create:
      file: scram-sha-user.yaml
      patch: '[{"op":"replace","path":"/spec/mongodbResourceRef/name","value": "sharded-cluster-scram-sha-256" }]'
      wait_until: in_updated_state
      timeout: 150
    """

    @classmethod
    def setup_class(cls):
        print(f"creating password for MongoDBUser {USER_NAME} in secret/{PASSWORD_SECRET_NAME} ")
        KubernetesTester.create_secret(
            KubernetesTester.get_namespace(),
            PASSWORD_SECRET_NAME,
            {
                "password": USER_PASSWORD,
            },
        )
        super().setup_class()

    def test_create_user(self):
        pass


@pytest.mark.e2e_sharded_cluster_scram_sha_256_user_connectivity
class TestShardedClusterIsUpdatedWithNewUser(KubernetesTester):
    def test_sharded_cluster_connectivity(self):
        ShardedClusterTester(MDB_RESOURCE, 2).assert_connectivity()

    def test_ops_manager_state_correctly_updated(self):
        expected_roles = {
            ("admin", "clusterAdmin"),
            ("admin", "userAdminAnyDatabase"),
            ("admin", "readWrite"),
            ("admin", "userAdminAnyDatabase"),
        }

        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_has_user(USER_NAME)
        tester.assert_user_has_roles(USER_NAME, expected_roles)
        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
        tester.assert_authentication_enabled()
        tester.assert_expected_users(1)

    def test_user_cannot_authenticate_with_incorrect_password(self):
        tester = ShardedClusterTester(MDB_RESOURCE, 2)
        tester.assert_scram_sha_authentication_fails(
            password="invalid-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-256",
        )

    def test_user_can_authenticate_with_correct_password(self):
        tester = ShardedClusterTester(MDB_RESOURCE, 2)
        tester.assert_scram_sha_authentication(
            password="my-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-256",
        )


@pytest.mark.e2e_sharded_cluster_scram_sha_256_user_connectivity
class TestCanChangePassword(KubernetesTester):
    @classmethod
    def setup_env(cls):
        print(f"updating password for MongoDBUser {USER_NAME} in secret/{PASSWORD_SECRET_NAME}")
        KubernetesTester.update_secret(
            KubernetesTester.get_namespace(),
            PASSWORD_SECRET_NAME,
            {"password": "my-new-password"},
        )

    def test_user_can_authenticate_with_new_password(self):
        tester = ShardedClusterTester(MDB_RESOURCE, 2)
        tester.assert_scram_sha_authentication(
            password="my-new-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-256",
        )

    def test_user_cannot_authenticate_with_old_password(self):
        tester = ShardedClusterTester(MDB_RESOURCE, 2)
        tester.assert_scram_sha_authentication_fails(
            password="my-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-256",
        )


@pytest.mark.e2e_sharded_cluster_scram_sha_256_user_connectivity
def test_credentials_secret_is_created(standard_secret: Dict[str, str]):
    assert "username" in standard_secret
    assert "password" in standard_secret
    assert "connectionString.standard" in standard_secret
    assert "connectionString.standardSrv" in standard_secret
    # authSource in the connection string must match the user's spec.db
    assert f"authSource={USER_DATABASE}" in standard_secret["connectionString.standard"]
    assert f"authSource={USER_DATABASE}" in standard_secret["connectionString.standardSrv"]


@pytest.mark.e2e_sharded_cluster_scram_sha_256_user_connectivity
def test_create_non_admin_db_user(namespace: str):
    create_or_update_secret(namespace, NON_ADMIN_PASSWORD_SECRET_NAME, {"password": NON_ADMIN_USER_PASSWORD})
    resource = MongoDBUser.from_yaml(find_fixture("scram-sha-user-non-admin-db.yaml"), namespace=namespace)
    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE
    try_load(resource)
    resource.update()
    resource.assert_reaches_phase(Phase.Updated, timeout=150)


@pytest.mark.e2e_sharded_cluster_scram_sha_256_user_connectivity
def test_non_admin_db_credentials_secret_is_created(non_admin_standard_secret: Dict[str, str]):
    assert "username" in non_admin_standard_secret
    assert "password" in non_admin_standard_secret
    assert "connectionString.standard" in non_admin_standard_secret
    assert "connectionString.standardSrv" in non_admin_standard_secret
    # authSource in the connection string must match the user's spec.db (non-admin database)
    assert f"authSource={NON_ADMIN_USER_DATABASE}" in non_admin_standard_secret["connectionString.standard"]
    assert f"authSource={NON_ADMIN_USER_DATABASE}" in non_admin_standard_secret["connectionString.standardSrv"]
