from os import environ

import pytest
from pytest import fixture

from kubetester.kubetester import (
    skip_if_local,
    fixture as yaml_fixture,
    KubernetesTester,
)
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager

OM_RESOURCE_NAME = "om-scram"
OM_USER_NAME = "mongodb-ops-manager"
USER_DEFINED_PASSWORD = "@my-scram-password#:"
UPDATED_USER_DEFINED_PASSWORD = f"updated-{USER_DEFINED_PASSWORD}"
AUTO_GENERATED_PASSWORD = ""
EXPECTED_OM_USER_ROLES = {
    ("admin", "readWriteAnyDatabase"),
    ("admin", "dbAdminAnyDatabase"),
    ("admin", "clusterMonitor"),
}


@fixture(scope="module")
def ops_manager(namespace) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_appdb_scram.yaml"), namespace=namespace
    )
    if "CUSTOM_OM_VERSION" in environ:
        resource["spec"]["version"] = environ.get("CUSTOM_OM_VERSION")

    return resource.create()


@pytest.mark.e2e_om_appdb_scram
class TestOpsManagerCreation:
    """
      Creates an Ops Manager instance with AppDB of size 3. This test waits until Ops Manager
      is ready to avoid changing password before Ops Manager has reached ready state
    """

    def test_appdb(self, ops_manager: MongoDBOpsManager):
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

        assert ops_manager.appdb_status().get_members() == 3
        assert ops_manager.appdb_status().get_version() == "4.0.0"

    def test_admin_config_map(self, ops_manager: MongoDBOpsManager):
        ops_manager.get_automation_config_tester().reached_version(1)

    def test_ops_manager_spec(self, ops_manager: MongoDBOpsManager):
        """ security (and authentication inside it) are not show in spec """
        assert "security" not in ops_manager["spec"]["applicationDatabase"]

    @skip_if_local
    def test_mongod(self, ops_manager: MongoDBOpsManager):
        mdb_tester = ops_manager.get_appdb_tester()
        mdb_tester.assert_connectivity()
        mdb_tester.assert_version("4.0.0")

    def test_appdb_automation_config(self, ops_manager: MongoDBOpsManager):
        # only user should be the Ops Manager user
        tester = ops_manager.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled("MONGODB-CR")
        tester.assert_has_user(OM_USER_NAME)
        tester.assert_user_has_roles(OM_USER_NAME, EXPECTED_OM_USER_ROLES)
        tester.assert_expected_users(1)
        tester.assert_authoritative_set(False)

    @skip_if_local
    def test_appdb_scram_sha(self, ops_manager: MongoDBOpsManager):
        app_db_tester = ops_manager.get_appdb_tester()

        global AUTO_GENERATED_PASSWORD
        AUTO_GENERATED_PASSWORD = ops_manager.read_appdb_generated_password()
        # should be possible to auth as the operator will have auto generated a password
        app_db_tester.assert_scram_sha_authentication(
            OM_USER_NAME, AUTO_GENERATED_PASSWORD, auth_mechanism="SCRAM-SHA-1"
        )

    def test_om_is_created(self, ops_manager: MongoDBOpsManager):
        ops_manager.om_status().assert_reaches_phase(phase=Phase.Running, timeout=700)
        # Let the monitoring get registered
        ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=100)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=300)


@pytest.mark.e2e_om_appdb_scram
class TestChangeOpsManagerUserPassword:
    """
      Creates a secret with a new password that the Ops Manager user should use and ensures that
      SCRAM is configured correctly with the new password
    """

    def test_upgrade_om(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        KubernetesTester.create_secret(
            ops_manager.namespace, "my-password", {"new-key": USER_DEFINED_PASSWORD}
        )

        ops_manager["spec"]["applicationDatabase"]["passwordSecretKeyRef"] = {
            "name": "my-password",
            "key": "new-key",
        }
        ops_manager.update()
        ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=100)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
        ops_manager.om_status().assert_abandons_phase(Phase.Running)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=800)

    @pytest.mark.xfail(
        reason="the auto generated password should have been deleted once the user creates their own"
    )
    def test_auto_generated_password_exists(self, ops_manager: MongoDBOpsManager):
        ops_manager.read_appdb_generated_password_secret()

    def test_config_map_reached_v2(self, ops_manager: MongoDBOpsManager):
        # should reach version 2 as a password has changed, resulting in new ScramShaCreds
        ops_manager.get_automation_config_tester().reached_version(2)

    def test_appdb_automation_config(self, ops_manager: MongoDBOpsManager):
        tester = ops_manager.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled("MONGODB-CR")
        tester.assert_has_user(OM_USER_NAME)
        tester.assert_user_has_roles(OM_USER_NAME, EXPECTED_OM_USER_ROLES)
        tester.assert_expected_users(1)
        tester.assert_authoritative_set(False)

    @skip_if_local
    def test_authenticate_with_user_password(self, ops_manager: MongoDBOpsManager):
        app_db_tester = ops_manager.get_appdb_tester()
        password = KubernetesTester.read_secret(ops_manager.namespace, "my-password")[
            "new-key"
        ]
        assert password == USER_DEFINED_PASSWORD
        app_db_tester.assert_scram_sha_authentication(
            OM_USER_NAME, password, auth_mechanism="SCRAM-SHA-1"
        )

    @skip_if_local
    def test_cannot_authenticate_with_old_autogenerated_password(
        self, ops_manager: MongoDBOpsManager
    ):
        app_db_tester = ops_manager.get_appdb_tester()
        app_db_tester.assert_scram_sha_authentication_fails(
            OM_USER_NAME, AUTO_GENERATED_PASSWORD, auth_mechanism="SCRAM-SHA-1"
        )


@pytest.mark.e2e_om_appdb_scram
class TestChangeOpsManagerExistingUserPassword:
    """
      Updating the secret should trigger another reconciliation because the
      Operator should be watching the user created secret.
    """

    def test_user_update_password(self, namespace: str):
        KubernetesTester.update_secret(
            namespace, "my-password", {"new-key": UPDATED_USER_DEFINED_PASSWORD},
        )

    def test_om_reconciled(self, ops_manager: MongoDBOpsManager):
        # OM got reconciled on secret change
        ops_manager.om_status().assert_abandons_phase(Phase.Running)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=800)

    @pytest.mark.xfail(
        reason="the auto generated password should have been deleted once the user creates their own"
    )
    def test_auto_generated_password_exists(self, ops_manager: MongoDBOpsManager):
        ops_manager.read_appdb_generated_password_secret()

    def test_config_map_reached_v3(self, ops_manager: MongoDBOpsManager):
        # should reach version 3 as a password has changed, resulting in new ScramShaCreds
        assert ops_manager.get_automation_config_tester().reached_version(3)

    def test_appdb_automation_config(self, ops_manager: MongoDBOpsManager):
        tester = ops_manager.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled("MONGODB-CR")
        tester.assert_has_user(OM_USER_NAME)
        tester.assert_user_has_roles(OM_USER_NAME, EXPECTED_OM_USER_ROLES)
        tester.assert_authoritative_set(False)
        tester.assert_expected_users(1)

    @skip_if_local
    def test_authenticate_with_user_password(self, ops_manager: MongoDBOpsManager):
        app_db_tester = ops_manager.get_appdb_tester()
        password = KubernetesTester.read_secret(ops_manager.namespace, "my-password")[
            "new-key"
        ]
        assert password == UPDATED_USER_DEFINED_PASSWORD
        app_db_tester.assert_scram_sha_authentication(
            OM_USER_NAME, password, auth_mechanism="SCRAM-SHA-1"
        )

    @skip_if_local
    def test_cannot_authenticate_with_old_autogenerated_password(
        self, ops_manager: MongoDBOpsManager
    ):
        app_db_tester = ops_manager.get_appdb_tester()
        app_db_tester.assert_scram_sha_authentication_fails(
            OM_USER_NAME, AUTO_GENERATED_PASSWORD, auth_mechanism="SCRAM-SHA-1"
        )


@pytest.mark.e2e_om_appdb_scram
class TestOpsManagerGeneratesNewPasswordIfNoneSpecified:
    """
    name: Fall back to auto generated password
    description: |
      Creates a secret with a new password that the Ops Manager user should use and ensures that
      SCRAM is configured correctly with the new password
    update:
      file: om_appdb_scram.yaml
      patch: '[{"op":"add","path":"/spec/applicationDatabase/passwordSecretKeyRef","value": {"name": "", "key": ""}}]'
      wait_until: om_in_running_state
      timeout: 1200
    """

    def test_upgrade_om(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["applicationDatabase"]["passwordSecretKeyRef"] = {
            "name": "",
            "key": "",
        }
        ops_manager.update()
        ops_manager.om_status().assert_abandons_phase(Phase.Running)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_new_password_was_created(self, ops_manager: MongoDBOpsManager):
        assert ops_manager.read_appdb_generated_password() != ""

    def test_wait_for_config_map_reached_v4(self, ops_manager: MongoDBOpsManager):
        # should reach version 4 as password should change back
        assert ops_manager.get_automation_config_tester().reached_version(4)

    @skip_if_local
    def test_cannot_authenticate_with_old_password(
        self, ops_manager: MongoDBOpsManager
    ):
        app_db_tester = ops_manager.get_appdb_tester()
        app_db_tester.assert_scram_sha_authentication_fails(
            OM_USER_NAME, USER_DEFINED_PASSWORD, auth_mechanism="SCRAM-SHA-1"
        )

    @skip_if_local
    def test_authenticate_with_user_password(self, ops_manager: MongoDBOpsManager):
        app_db_tester = ops_manager.get_appdb_tester()
        password = ops_manager.read_appdb_generated_password()
        assert (
            password != AUTO_GENERATED_PASSWORD
        ), "new password should have been generated"
        app_db_tester.assert_scram_sha_authentication(
            OM_USER_NAME, password, auth_mechanism="SCRAM-SHA-1"
        )
