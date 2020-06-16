import pytest

from kubetester.omtester import get_rs_cert_names
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester
from kubetester.mongotester import ReplicaSetTester

from kubetester.mongodb import MongoDB, Phase
from pytest import fixture

MDB_RESOURCE = "replica-set-x509-to-scram-256"
USER_NAME = "mms-user-1"
PASSWORD_SECRET_NAME = "mms-user-1-password"
USER_PASSWORD = "my-password"


@fixture(scope="module")
def replica_set(namespace: str) -> MongoDB:
    return MongoDB(MDB_RESOURCE, namespace=namespace).load()


@pytest.mark.e2e_replica_set_x509_to_scram_transition
class TestEnableX509ForReplicaSet(KubernetesTester):
    """
    description: |
      Creates a Replica Set with X509 authentication enabled
    create:
      file: replica-set-x509-to-scram-256.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
    """

    def test_approve_certs(self):
        for cert in self.yield_existing_csrs(
            get_rs_cert_names(MDB_RESOURCE, self.namespace, with_agent_certs=True)
        ):
            print("Approving certificate {}".format(cert))
            self.approve_certificate(cert)
        KubernetesTester.wait_until("in_running_state")

    def test_ops_manager_state_updated_correctly(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_enabled("MONGODB-X509")
        tester.assert_authentication_enabled()


@pytest.mark.e2e_replica_set_x509_to_scram_transition
def test_enable_scram_and_x509(replica_set: MongoDB):
    replica_set.load()
    replica_set["spec"]["security"]["authentication"]["modes"] = ["X509", "SCRAM"]
    replica_set.update()
    replica_set.assert_abandons_phase(Phase.Running, timeout=100)
    replica_set.assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_replica_set_x509_to_scram_transition
def test_x509_is_still_configured(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=300)
    tester = AutomationConfigTester(KubernetesTester.get_automation_config())
    tester.assert_authentication_mechanism_enabled("MONGODB-X509")
    tester.assert_authentication_mechanism_enabled(
        "SCRAM-SHA-256", active_auth_mechanism=False
    )
    tester.assert_authentication_enabled(expected_num_deployment_auth_mechanisms=2)


@pytest.mark.e2e_replica_set_x509_to_scram_transition
class TestReplicaSetDisableAuthentication(KubernetesTester):
    def test_disable_auth(self, replica_set: MongoDB):
        replica_set.load()
        replica_set["spec"]["security"]["authentication"]["enabled"] = False
        replica_set.update()
        replica_set.assert_abandons_phase(Phase.Running, timeout=100)
        replica_set.assert_reaches_phase(Phase.Running, timeout=900)

    def test_assert_connectivity(self, replica_set: MongoDB):
        replica_set.tester().assert_connectivity()

    def test_ops_manager_state_updated_correctly(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_disabled("MONGODB-X509")
        tester.assert_authentication_mechanism_disabled("SCRAM-SHA-256")
        tester.assert_authentication_disabled()


@pytest.mark.e2e_replica_set_x509_to_scram_transition
class TestCanEnableScramSha256:
    def test_can_enable_scram_sha_256(self, replica_set: MongoDB):
        replica_set.load()
        replica_set["spec"]["security"]["authentication"]["enabled"] = True
        replica_set["spec"]["security"]["authentication"]["modes"] = ["SCRAM"]
        replica_set["spec"]["security"]["authentication"]["agents"]["mode"] = "SCRAM"
        replica_set.update()
        replica_set.assert_abandons_phase(Phase.Running, timeout=100)
        replica_set.assert_reaches_phase(Phase.Running, timeout=900)

    def test_assert_connectivity(self):
        ReplicaSetTester(MDB_RESOURCE, 3, ssl=True).assert_connectivity()

    def test_ops_manager_state_updated_correctly(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_disabled("MONGODB-X509")
        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
        tester.assert_authentication_enabled()


@pytest.mark.e2e_replica_set_x509_to_scram_transition
class TestCreateScramSha256User(KubernetesTester):
    """
    description: |
      Creates a SCRAM-SHA-256 user
    create:
      file: scram-sha-user.yaml
      patch: '[{"op":"replace","path":"/spec/mongodbResourceRef/name","value": "replica-set-x509-to-scram-256" }]'
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

    def test_user_cannot_authenticate_with_incorrect_password(self):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_scram_sha_authentication_fails(
            password="invalid-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-256",
            ssl=True,
        )

    def test_user_can_authenticate_with_correct_password(self):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_scram_sha_authentication(
            password="my-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-256",
            ssl=True,
        )


@pytest.mark.e2e_replica_set_x509_to_scram_transition
class TestReplicaSetDeleted(KubernetesTester):
    """
    description: |
      Deletes the Replica Set
    delete:
      file: replica-set-x509-to-scram-256.yaml
      wait_until: mongo_resource_deleted
      timeout: 240
    """

    def test_noop(self):
        pass
