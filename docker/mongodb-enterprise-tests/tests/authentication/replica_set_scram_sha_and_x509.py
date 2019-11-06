import pytest

from kubetester.kubetester import KubernetesTester
from kubetester.mongotester import ReplicaSetTester
from kubetester.omtester import get_rs_cert_names
from kubetester.automation_config_tester import AutomationConfigTester

MDB_RESOURCE = "replica-set-scram-256-and-x509"
USER_NAME = "mms-user-1"
PASSWORD_SECRET_NAME = "mms-user-1-password"
USER_PASSWORD = "my-password"


@pytest.mark.e2e_replica_set_scram_sha_and_x509
class TestReplicaSetCreation(KubernetesTester):
    """
    description: |
      Creates a Replica set and checks everything is created as expected.
    create:
      file: replica-set-tls-scram-sha-256.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
      timeout: 120
    """

    def test_approve_certificates(self):
        for cert in self.yield_existing_csrs(
                get_rs_cert_names(MDB_RESOURCE, self.namespace)):
            self.approve_certificate(cert)
        KubernetesTester.wait_until(KubernetesTester.in_running_state)

    def test_replica_set_connectivity(self):
        ReplicaSetTester(MDB_RESOURCE, 3, ssl=True).assert_connectivity()

    def test_ops_manager_state_correctly_updated(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
        tester.assert_authentication_enabled()


@pytest.mark.e2e_replica_set_scram_sha_and_x509
class TestCreateMongoDBUser(KubernetesTester):
    """
    description: |
      Creates a MongoDBUser
    create:
      file: scram-sha-user.yaml
      patch: '[{"op":"replace","path":"/spec/mongodbResourceRef/name","value": "replica-set-scram-256-and-x509" }]'
      wait_until: in_updated_state
      timeout: 150
    """

    @classmethod
    def setup_class(cls):
        print(f"creating password for MongoDBUser {USER_NAME} in secret/{PASSWORD_SECRET_NAME} ")
        KubernetesTester.create_secret(KubernetesTester.get_namespace(), PASSWORD_SECRET_NAME, {
            "password": USER_PASSWORD,
        })
        super().setup_class()

    def test_create_user(self):
        pass


@pytest.mark.e2e_replica_set_scram_sha_and_x509
class TestScramUserCanAuthenticate(KubernetesTester):
    def test_user_cannot_authenticate_with_incorrect_password(self):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_scram_sha_authentication_fails(password="invalid-password", username="mms-user-1", ssl=True,
                                                     auth_mechanism="SCRAM-SHA-256")

    def test_user_can_authenticate_with_correct_password(self):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_scram_sha_authentication(password="my-password", username="mms-user-1", ssl=True,
                                               auth_mechanism="SCRAM-SHA-256")


@pytest.mark.e2e_replica_set_scram_sha_and_x509
class TestEnableX509(KubernetesTester):
    """
    update:
      file: replica-set-tls-scram-sha-256.yaml
      patch: '[{"op":"replace","path":"/spec/security/authentication/modes", "value": ["X509", "SCRAM"]}]'
      wait_until: in_running_state
    """

    # important note that no CSRs for the agents should have been created
    def test_ops_manager_state_correctly_updated(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config(), expected_users=3)
        # when both scram and x509 are enabled, agents should be using scram
        tester.assert_authentication_mechanism_enabled("MONGODB-X509", active_auth_mechanism=False)
        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
        tester.assert_authentication_enabled(expected_num_deployment_auth_mechanisms=2)


@pytest.mark.e2e_replica_set_scram_sha_and_x509
class TestAddMongoDBUser(KubernetesTester):
    """
    create:
      file: test-x509-user.yaml
      patch: '[{"op":"replace","path":"/spec/mongodbResourceRef/name","value": "replica-set-scram-256-and-x509" }]'
      wait_until: user_exists
    """

    def test_add_user(self):
        assert True

    @staticmethod
    def user_exists():
        ac = KubernetesTester.get_automation_config()
        users = ac['auth']['usersWanted']
        return 'CN=x509-testing-user' in [user['user'] for user in users]


@pytest.mark.e2e_replica_set_scram_sha_and_x509
class TestX509CertCreationAndApproval(KubernetesTester):
    def setup(self):
        cert_name = 'x509-testing-user.' + self.get_namespace()
        self.cert_file = self.generate_certfile(cert_name, 'x509-testing-user.csr',
                                                'server-key.pem')

    def teardown(self):
        self.cert_file.close()

    def test_can_authenticate_with_added_user(self):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_x509_authentication(cert_file_name=self.cert_file.name)


@pytest.mark.e2e_replica_set_scram_sha_and_x509
class TestCanStillAuthAsScramUsers(KubernetesTester):
    def test_user_cannot_authenticate_with_incorrect_password(self):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_scram_sha_authentication_fails(password="invalid-password", username="mms-user-1", ssl=True,
                                                     auth_mechanism="SCRAM-SHA-256")

    def test_user_can_authenticate_with_correct_password(self):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_scram_sha_authentication(password="my-password", username="mms-user-1", ssl=True,
                                               auth_mechanism="SCRAM-SHA-256")
