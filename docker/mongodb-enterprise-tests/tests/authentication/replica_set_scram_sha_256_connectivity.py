import pytest

from kubetester.kubetester import KubernetesTester
from kubetester.mongotester import ReplicaSetTester
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.mongodb import MongoDB, Phase

MDB_RESOURCE = "my-replica-set"
USER_NAME = "mms-user-1"
PASSWORD_SECRET_NAME = "mms-user-1-password"
USER_PASSWORD = "my-password"


@pytest.mark.e2e_replica_set_scram_sha_256_user_connectivity
class TestReplicaSetCreation(KubernetesTester):
    """
    description: |
      Creates a Replica set and checks everything is created as expected.
    create:
      file: replica-set-scram-sha-256.yaml
      patch: '[{"op":"replace","path":"/spec/security/authentication", "value" : {"ignoreUnknownUsers": true , "enabled" : true, "modes" : ["SCRAM"]}} ]'
      wait_until: in_running_state
    """

    def test_replica_set_connectivity(self):
        ReplicaSetTester(MDB_RESOURCE, 3).assert_connectivity()

    def test_ops_manager_state_correctly_updated(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
        tester.assert_authentication_enabled()
        tester.assert_expected_users(2)
        tester.assert_authoritative_set(False)


@pytest.mark.e2e_replica_set_scram_sha_256_user_connectivity
class TestCreateMongoDBUser(KubernetesTester):
    """
    description: |
      Creates a MongoDBUser
    create:
      file: scram-sha-user.yaml
      wait_until: in_updated_state
      timeout: 150
    """

    @classmethod
    def setup_class(cls):
        print(
            f"creating password for MongoDBUser {USER_NAME} in secret/{PASSWORD_SECRET_NAME} "
        )
        KubernetesTester.create_secret(
            KubernetesTester.get_namespace(),
            PASSWORD_SECRET_NAME,
            {"password": USER_PASSWORD,},
        )
        super().setup_class()

    def test_create_user(self):
        pass


@pytest.mark.e2e_replica_set_scram_sha_256_user_connectivity
class TestReplicaSetIsUpdatedWithNewUser(KubernetesTester):
    def test_replica_set_connectivity(self):
        ReplicaSetTester(MDB_RESOURCE, 3).assert_connectivity()

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
        tester.assert_expected_users(3)
        tester.assert_authoritative_set(False)

    def test_user_cannot_authenticate_with_incorrect_password(self):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_scram_sha_authentication_fails(
            password="invalid-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-256",
        )

    def test_user_can_authenticate_with_correct_password(self):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_scram_sha_authentication(
            password="my-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-256",
        )


@pytest.mark.e2e_replica_set_scram_sha_256_user_connectivity
class TestCanChangePassword(KubernetesTester):
    @classmethod
    def setup_env(cls):
        print(
            f"updating password for MongoDBUser {USER_NAME} in secret/{PASSWORD_SECRET_NAME}"
        )
        KubernetesTester.update_secret(
            KubernetesTester.get_namespace(),
            PASSWORD_SECRET_NAME,
            {"password": "my-new-password"},
        )

    def test_user_can_authenticate_with_new_password(self):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_scram_sha_authentication(
            password="my-new-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-256",
        )

    def test_user_cannot_authenticate_with_old_password(self):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_scram_sha_authentication_fails(
            password="my-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-256",
        )


@pytest.mark.e2e_replica_set_scram_sha_256_user_connectivity
def test_authentication_is_still_configured_after_remove_authentication(namespace: str):
    replica_set = MongoDB(name=MDB_RESOURCE, namespace=namespace).load()
    replica_set["spec"]["security"]["authentication"] = None
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    tester = replica_set.get_automation_config_tester()
    # authentication remains enabled as the operator is not configuring it when
    # spec.security.authentication is not configured
    tester.assert_has_user(USER_NAME)
    tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
    tester.assert_authentication_enabled()
    tester.assert_expected_users(3)
    tester.assert_authoritative_set(False)


@pytest.mark.e2e_replica_set_scram_sha_256_user_connectivity
def test_authentication_can_be_disabled_without_modes(namespace: str):
    replica_set = MongoDB(name=MDB_RESOURCE, namespace=namespace).load()
    replica_set["spec"]["security"]["authentication"] = {
        "enabled": False,
    }
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    tester = replica_set.get_automation_config_tester()
    # we have explicitly set authentication to be disabled
    tester.assert_has_user(USER_NAME)
    tester.assert_authentication_disabled(remaining_users=1)
