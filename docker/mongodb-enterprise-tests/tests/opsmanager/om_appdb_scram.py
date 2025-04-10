from typing import Optional

import pytest
from kubetester import create_or_update_secret
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture
from tests.conftest import get_central_cluster_client, is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

OM_RESOURCE_NAME = "om-scram"
OM_USER_NAME = "mongodb-ops-manager"
USER_DEFINED_PASSWORD = "@my-scram-password#:"
UPDATED_USER_DEFINED_PASSWORD = f"updated-{USER_DEFINED_PASSWORD}"
EXPECTED_OM_USER_ROLES = {
    ("admin", "readWriteAnyDatabase"),
    ("admin", "dbAdminAnyDatabase"),
    ("admin", "clusterMonitor"),
    ("admin", "hostManager"),
    ("admin", "backup"),
    ("admin", "restore"),
}


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(yaml_fixture("om_appdb_scram.yaml"), namespace=namespace)
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    resource.update()
    return resource


@fixture(scope="module")
def auto_generated_password(ops_manager: MongoDBOpsManager) -> str:
    return ops_manager.read_appdb_generated_password()


@pytest.mark.e2e_om_appdb_scram
class TestOpsManagerCreation:
    """
    Creates an Ops Manager instance with AppDB of size 3. This test waits until Ops Manager
    is ready to avoid changing password before Ops Manager has reached ready state
    """

    def test_appdb(self, ops_manager: MongoDBOpsManager, custom_appdb_version: str):
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running)

        assert ops_manager.appdb_status().get_members() == 3
        assert ops_manager.appdb_status().get_version() == custom_appdb_version

    def test_admin_config_map(self, ops_manager: MongoDBOpsManager):
        ops_manager.get_automation_config_tester().reached_version(1)

    def test_ops_manager_spec(self, ops_manager: MongoDBOpsManager):
        """security (and authentication inside it) are not show in spec"""
        assert "security" not in ops_manager["spec"]["applicationDatabase"]

    def test_mongod(self, ops_manager: MongoDBOpsManager, custom_appdb_version: str):
        mdb_tester = ops_manager.get_appdb_tester()
        mdb_tester.assert_connectivity()
        mdb_tester.assert_version(custom_appdb_version)

    def test_appdb_automation_config(self, ops_manager: MongoDBOpsManager):
        # only user should be the Ops Manager user
        tester = ops_manager.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256", False)
        tester.assert_has_user(OM_USER_NAME)
        tester.assert_user_has_roles(OM_USER_NAME, EXPECTED_OM_USER_ROLES)
        tester.assert_expected_users(1)
        tester.assert_authoritative_set(False)

    def test_scram_secrets_exists_with_correct_owner_reference(self, ops_manager: MongoDBOpsManager):
        password_secret = ops_manager.read_appdb_agent_password_secret()
        keyfile_secret = ops_manager.read_appdb_agent_keyfile_secret()
        omUID = ops_manager.backing_obj["metadata"]["uid"]

        assert len(password_secret.metadata.owner_references) == 1
        assert password_secret.metadata.owner_references[0].uid == omUID

        assert len(keyfile_secret.metadata.owner_references) == 1
        assert keyfile_secret.metadata.owner_references[0].uid == omUID

    def test_appdb_scram_sha(self, ops_manager: MongoDBOpsManager, auto_generated_password: str):
        app_db_tester = ops_manager.get_appdb_tester()

        # should be possible to auth as the operator will have auto generated a password
        app_db_tester.assert_scram_sha_authentication(
            OM_USER_NAME, auto_generated_password, auth_mechanism="SCRAM-SHA-256"
        )

    def test_om_is_created(self, ops_manager: MongoDBOpsManager):
        ops_manager.om_status().assert_reaches_phase(phase=Phase.Running)
        # Let the monitoring get registered
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running)


@pytest.mark.e2e_om_appdb_scram
class TestChangeOpsManagerUserPassword:
    """
    Creates a secret with a new password that the Ops Manager user should use and ensures that
    SCRAM is configured correctly with the new password
    """

    def test_upgrade_om(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        create_or_update_secret(
            ops_manager.namespace,
            "my-password",
            {"new-key": USER_DEFINED_PASSWORD},
            api_client=get_central_cluster_client(),
        )

        ops_manager["spec"]["applicationDatabase"]["passwordSecretKeyRef"] = {
            "name": "my-password",
            "key": "new-key",
        }
        ops_manager.update()

        # Swapping the password can lead to a race where we check for the status before om reconciler was able to swap
        # the password.
        ops_manager.om_status().assert_reaches_phase(Phase.Running, ignore_errors=True)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running)

    @pytest.mark.xfail(reason="the auto generated password should have been deleted once the user creates their own")
    def test_auto_generated_password_exists(self, ops_manager: MongoDBOpsManager):
        ops_manager.read_appdb_generated_password_secret()

    def test_config_map_reached_v2(self, ops_manager: MongoDBOpsManager):
        # should reach version 2 as a password has changed, resulting in new ScramShaCreds
        ops_manager.get_automation_config_tester().reached_version(2)

    def test_appdb_automation_config(self, ops_manager: MongoDBOpsManager):
        tester = ops_manager.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256", False)
        tester.assert_has_user(OM_USER_NAME)
        tester.assert_user_has_roles(OM_USER_NAME, EXPECTED_OM_USER_ROLES)
        tester.assert_expected_users(1)
        tester.assert_authoritative_set(False)

    def test_authenticate_with_user_password(self, ops_manager: MongoDBOpsManager):
        app_db_tester = ops_manager.get_appdb_tester()
        password = KubernetesTester.read_secret(ops_manager.namespace, "my-password")["new-key"]
        assert password == USER_DEFINED_PASSWORD
        app_db_tester.assert_scram_sha_authentication(OM_USER_NAME, password, auth_mechanism="SCRAM-SHA-256")

    def test_cannot_authenticate_with_old_autogenerated_password(
        self, ops_manager: MongoDBOpsManager, auto_generated_password: str
    ):
        app_db_tester = ops_manager.get_appdb_tester()
        app_db_tester.assert_scram_sha_authentication_fails(
            OM_USER_NAME, auto_generated_password, auth_mechanism="SCRAM-SHA-256"
        )


@pytest.mark.e2e_om_appdb_scram
class TestChangeOpsManagerExistingUserPassword:
    """
    Updating the secret should trigger another reconciliation because the
    Operator should be watching the user created secret.
    """

    def test_user_update_password(self, namespace: str):
        KubernetesTester.update_secret(
            namespace,
            "my-password",
            {"new-key": UPDATED_USER_DEFINED_PASSWORD},
        )

    def test_om_reconciled(self, ops_manager: MongoDBOpsManager):
        ops_manager.appdb_status().assert_abandons_phase(Phase.Running)
        ops_manager.om_status().assert_abandons_phase(Phase.Running)
        # Swapping the password can lead to a race where we check for the status before om reconciler was able to swap
        # the password.
        ops_manager.om_status().assert_reaches_phase(Phase.Running, ignore_errors=True)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running)

    @pytest.mark.xfail(reason="the auto generated password should have been deleted once the user creates their own")
    def test_auto_generated_password_exists(self, ops_manager: MongoDBOpsManager):
        ops_manager.read_appdb_generated_password_secret()

    def test_config_map_reached_v3(self, ops_manager: MongoDBOpsManager):
        KubernetesTester.wait_until(
            lambda: ops_manager.get_automation_config_tester().reached_version(3),
            timeout=180,
        )

    def test_appdb_automation_config(self, ops_manager: MongoDBOpsManager):
        tester = ops_manager.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256", False)
        tester.assert_has_user(OM_USER_NAME)
        tester.assert_user_has_roles(OM_USER_NAME, EXPECTED_OM_USER_ROLES)
        tester.assert_authoritative_set(False)
        tester.assert_expected_users(1)

    def test_authenticate_with_user_password(self, ops_manager: MongoDBOpsManager):
        app_db_tester = ops_manager.get_appdb_tester()
        password = KubernetesTester.read_secret(ops_manager.namespace, "my-password")["new-key"]
        assert password == UPDATED_USER_DEFINED_PASSWORD
        app_db_tester.assert_scram_sha_authentication(OM_USER_NAME, password, auth_mechanism="SCRAM-SHA-256")

    def test_cannot_authenticate_with_old_autogenerated_password(
        self, ops_manager: MongoDBOpsManager, auto_generated_password: str
    ):
        app_db_tester = ops_manager.get_appdb_tester()
        app_db_tester.assert_scram_sha_authentication_fails(
            OM_USER_NAME, auto_generated_password, auth_mechanism="SCRAM-SHA-256"
        )


@pytest.mark.e2e_om_appdb_scram
class TestOpsManagerGeneratesNewPasswordIfNoneSpecified:
    def test_upgrade_om(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["applicationDatabase"]["passwordSecretKeyRef"] = {
            "name": "",
            "key": "",
        }
        ops_manager.update()
        # Swapping the password can lead to a race when the Agent deploys new password to AppDB and the
        # Operator updates the connection string (which leads to restarting OM). Since there are no time
        # or order guarantees, the entire system may go through an error state. In that case,
        # the recovery will happen soon, but it needs a bit more time.
        ops_manager.om_status().assert_reaches_phase(Phase.Running, ignore_errors=True, timeout=1800)

    def test_new_password_was_created(self, ops_manager: MongoDBOpsManager):
        assert ops_manager.read_appdb_generated_password() != ""

    def test_wait_for_config_map_reached_v4(self, ops_manager: MongoDBOpsManager):
        # should reach version 4 as password should change back
        assert ops_manager.get_automation_config_tester().reached_version(4)

    def test_cannot_authenticate_with_old_password(self, ops_manager: MongoDBOpsManager):
        app_db_tester = ops_manager.get_appdb_tester()
        app_db_tester.assert_scram_sha_authentication_fails(
            OM_USER_NAME, USER_DEFINED_PASSWORD, auth_mechanism="SCRAM-SHA-256"
        )

    def test_authenticate_with_user_password(self, ops_manager: MongoDBOpsManager, auto_generated_password: str):
        app_db_tester = ops_manager.get_appdb_tester()
        password = ops_manager.read_appdb_generated_password()
        assert password != auto_generated_password, "new password should have been generated"
        app_db_tester.assert_scram_sha_authentication(OM_USER_NAME, password, auth_mechanism="SCRAM-SHA-256")
