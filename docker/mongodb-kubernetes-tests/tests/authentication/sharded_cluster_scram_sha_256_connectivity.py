from typing import Dict

import pytest
from kubetester import create_or_update_secret, find_fixture, read_secret, try_load, wait_until
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.mongotester import MongoTester, ShardedClusterTester
from kubetester.phase import Phase
from kubetester.scram import (
    assert_creds_preserved,
    assert_user_mechanisms,
    build_scram_user_resource,
    build_sha256_creds,
    get_ac_user,
    seed_user_in_ac,
)
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
def test_credentials_can_connect_to_db(standard_secret: Dict[str, str]):
    MongoTester(standard_secret["connectionString.standard"], use_ssl=False).assert_connectivity()


@pytest.mark.e2e_sharded_cluster_scram_sha_256_user_connectivity
def test_credentials_can_connect_to_db_with_srv(standard_secret: Dict[str, str]):
    MongoTester(standard_secret["connectionString.standardSrv"], use_ssl=False).assert_connectivity()


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


@pytest.mark.e2e_sharded_cluster_scram_sha_256_user_connectivity
def test_non_admin_credentials_can_connect_to_db(non_admin_standard_secret: Dict[str, str]):
    MongoTester(non_admin_standard_secret["connectionString.standard"], use_ssl=False).assert_connectivity()


@pytest.mark.e2e_sharded_cluster_scram_sha_256_user_connectivity
def test_non_admin_credentials_can_connect_to_db_with_srv(non_admin_standard_secret: Dict[str, str]):
    MongoTester(non_admin_standard_secret["connectionString.standardSrv"], use_ssl=False).assert_connectivity()


def _seed_sha256_user_in_ac() -> None:
    mdb = MongoDB(MDB_RESOURCE, KubernetesTester.get_namespace())
    mdb.load()
    seed_user_in_ac(
        om_tester=mdb.get_om_tester(),
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


def _seed_no_mech_user_in_ac() -> None:
    mdb = MongoDB(MDB_RESOURCE, KubernetesTester.get_namespace())
    mdb.load()
    seed_user_in_ac(
        om_tester=mdb.get_om_tester(),
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


@pytest.mark.e2e_sharded_cluster_scram_sha_256_user_connectivity
class TestK8sUserHasEmptyMechanisms(KubernetesTester):
    def test_k8s_user_mechanisms_empty_in_ac(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_has_user(USER_NAME)
        assert_user_mechanisms(tester, USER_NAME, [])

    def test_k8s_user_has_both_creds(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        user = get_ac_user(tester, USER_NAME)
        assert user.get("scramSha256Creds"), "scramSha256Creds should be present"
        assert user.get("scramSha1Creds"), "scramSha1Creds should be present"


# Tests importing a user that already exists in Ops Manager into K8s management.
# The user has only SHA-256 creds. The two setup steps below must run in order:
# step 1 creates the user directly in Ops Manager, step 2 registers it with the operator
# via a MongoDBUser resource. TestOMUserSha256OnlyPreserved then checks that the original
# SHA-256 creds are kept intact and that the operator generates the missing SHA-1 creds.


@pytest.mark.e2e_sharded_cluster_scram_sha_256_user_connectivity
def test_seed_sha256_user_in_ac():
    _seed_sha256_user_in_ac()


@pytest.mark.e2e_sharded_cluster_scram_sha_256_user_connectivity
def test_om_user_sha256_created(namespace: str):
    resource = _build_sha256_user_in_k8s(namespace)
    resource.update()
    resource.assert_reaches_phase(Phase.Updated)


@pytest.mark.e2e_sharded_cluster_scram_sha_256_user_connectivity
class TestOMUserSha256OnlyPreserved(KubernetesTester):
    def test_om_user_sha256_mechanisms_empty_after_transition(self):
        # Once Ops Manager processes the password and the operator reconciles again,
        # it treats the imported user as K8s-managed (mechanisms=[]).
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_has_user(OM_SHA256_USER_NAME)
        assert_user_mechanisms(tester, OM_SHA256_USER_NAME, [])

    def test_om_user_sha256_creds_preserved_byte_for_byte(self):
        # The SHA-256 creds set in Ops Manager before the import must remain unchanged.
        # Only the missing SHA-1 creds should be added.
        assert_creds_preserved(
            AutomationConfigTester(KubernetesTester.get_automation_config()),
            OM_SHA256_USER_NAME,
            sha256_creds=SEEDED_SHA256_CREDS,
        )

    def test_om_user_sha256_gets_sha1_creds_after_transition(self):
        # After the import the operator generates only the missing SHA-1 creds.
        user = get_ac_user(AutomationConfigTester(KubernetesTester.get_automation_config()), OM_SHA256_USER_NAME)
        assert user.get("scramSha256Creds"), "SHA-256 creds must be present"
        assert user.get("scramSha1Creds"), "SHA-1 creds must be present after the follow-up reconcile"

    def test_om_user_sha256_can_authenticate_after_transition(self):
        ShardedClusterTester(MDB_RESOURCE, 2).assert_scram_sha_authentication(
            password=OM_SHA256_USER_PASSWORD,
            username=OM_SHA256_USER_NAME,
            auth_mechanism="SCRAM-SHA-256",
            attempts=20,
        )

    def test_om_user_sha256_password_can_change(self):
        ac_version = AutomationConfigTester(KubernetesTester.get_automation_config()).automation_config["version"]
        new_password = "om-sha256-password-new-1"
        KubernetesTester.update_secret(
            KubernetesTester.get_namespace(), OM_SHA256_USER_PASSWORD_SECRET, {"password": new_password}
        )

        wait_until(
            lambda: AutomationConfigTester(KubernetesTester.get_automation_config()).reached_version(ac_version + 1),
            timeout=600,
        )

        assert_user_mechanisms(
            AutomationConfigTester(KubernetesTester.get_automation_config()), OM_SHA256_USER_NAME, []
        )
        ShardedClusterTester(MDB_RESOURCE, 2).assert_scram_sha_authentication(
            password=new_password,
            username=OM_SHA256_USER_NAME,
            auth_mechanism="SCRAM-SHA-256",
        )


# Same import scenario as above, but the user has no mechanisms list set in Ops Manager.
# The operator treats this the same as a K8s-managed user: it keeps the existing SHA-256
# creds and generates the missing SHA-1 creds in the same reconcile pass.


@pytest.mark.e2e_sharded_cluster_scram_sha_256_user_connectivity
def test_seed_no_mech_user_in_ac():
    _seed_no_mech_user_in_ac()


@pytest.mark.e2e_sharded_cluster_scram_sha_256_user_connectivity
def test_om_user_no_mech_created(namespace: str):
    resource = _build_no_mech_user_in_k8s(namespace)
    resource.update()
    resource.assert_reaches_phase(Phase.Updated)


@pytest.mark.e2e_sharded_cluster_scram_sha_256_user_connectivity
class TestOMUserNullMechanismsIsK8sManaged(KubernetesTester):
    def test_om_user_no_mech_mechanisms_empty_after_transition(self):
        # Once Ops Manager processes the password and the operator reconciles again,
        # it treats the imported user as K8s-managed (mechanisms=[]).
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_has_user(OM_NO_MECH_USER_NAME)
        assert_user_mechanisms(tester, OM_NO_MECH_USER_NAME, [])

    def test_om_user_no_mech_sha256_creds_preserved(self):
        # The SHA-256 creds set in Ops Manager before the import must remain unchanged.
        # Only the missing SHA-1 creds should be added.
        assert_creds_preserved(
            AutomationConfigTester(KubernetesTester.get_automation_config()),
            OM_NO_MECH_USER_NAME,
            sha256_creds=SEEDED_NO_MECH_SHA256_CREDS,
        )

    def test_om_user_no_mech_gets_sha1_creds_after_transition(self):
        # After the import the operator generates only the missing SHA-1 creds.
        user = get_ac_user(AutomationConfigTester(KubernetesTester.get_automation_config()), OM_NO_MECH_USER_NAME)
        assert user.get("scramSha256Creds"), "SHA-256 creds must be present"
        assert user.get("scramSha1Creds"), "SHA-1 creds must be present after the follow-up reconcile"

    def test_om_user_no_mech_can_authenticate_after_transition(self):
        ShardedClusterTester(MDB_RESOURCE, 2).assert_scram_sha_authentication(
            password=OM_NO_MECH_USER_PASSWORD,
            username=OM_NO_MECH_USER_NAME,
            auth_mechanism="SCRAM-SHA-256",
            attempts=20,
        )

    def test_om_user_no_mech_password_can_change(self):
        ac_version = AutomationConfigTester(KubernetesTester.get_automation_config()).automation_config["version"]
        new_password = "om-no-mech-password-new-1"
        KubernetesTester.update_secret(
            KubernetesTester.get_namespace(), OM_NO_MECH_USER_PASSWORD_SECRET, {"password": new_password}
        )

        wait_until(
            lambda: AutomationConfigTester(KubernetesTester.get_automation_config()).reached_version(ac_version + 1),
            timeout=600,
        )

        assert_user_mechanisms(
            AutomationConfigTester(KubernetesTester.get_automation_config()), OM_NO_MECH_USER_NAME, []
        )
        ShardedClusterTester(MDB_RESOURCE, 2).assert_scram_sha_authentication(
            password=new_password,
            username=OM_NO_MECH_USER_NAME,
            auth_mechanism="SCRAM-SHA-256",
        )
