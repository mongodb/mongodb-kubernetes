import tempfile

import pytest
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs import (
    ISSUER_CA_NAME,
    create_mongodb_tls_certs,
    create_x509_user_cert,
)
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongotester import ReplicaSetTester
from kubetester.phase import Phase

MDB_RESOURCE = "replica-set-scram-256-and-x509"
USER_NAME = "mms-user-1"
PASSWORD_SECRET_NAME = "mms-user-1-password"
USER_PASSWORD = "my-password"


@pytest.fixture(scope="module")
def replica_set(namespace: str, issuer_ca_configmap: str, server_certs: str) -> MongoDB:
    res = MongoDB.from_yaml(load_fixture("replica-set-tls-scram-sha-256.yaml"), namespace=namespace)
    res["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    return res.create()


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    return create_mongodb_tls_certs(ISSUER_CA_NAME, namespace, MDB_RESOURCE, f"{MDB_RESOURCE}-cert")


@pytest.mark.e2e_replica_set_scram_sha_and_x509
class TestReplicaSetCreation(KubernetesTester):
    def test_replica_set_running(self, replica_set: MongoDB):
        replica_set.assert_reaches_phase(Phase.Running, timeout=400)

    def test_replica_set_connectivity(self, replica_set: MongoDB, ca_path: str):
        tester = replica_set.tester(use_ssl=True, ca_path=ca_path)
        tester.assert_connectivity()

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


@pytest.mark.e2e_replica_set_scram_sha_and_x509
class TestScramUserCanAuthenticate(KubernetesTester):
    def test_user_cannot_authenticate_with_incorrect_password(self, ca_path: str):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_scram_sha_authentication_fails(
            password="invalid-password",
            username="mms-user-1",
            ssl=True,
            auth_mechanism="SCRAM-SHA-256",
            tlsCAFile=ca_path,
        )

    def test_user_can_authenticate_with_correct_password(self, ca_path):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_scram_sha_authentication(
            password="my-password",
            username="mms-user-1",
            ssl=True,
            auth_mechanism="SCRAM-SHA-256",
            tlsCAFile=ca_path,
        )

    def test_enable_x509(self, replica_set: MongoDB):
        replica_set.load()
        replica_set["spec"]["security"]["authentication"]["modes"].append("X509")
        replica_set["spec"]["security"]["authentication"]["agents"] = {"mode": "SCRAM"}
        replica_set.update()
        replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    def test_automation_config_was_updated(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        # when both agents.mode is set to SCRAM, X509 should not be used as agent auth
        tester.assert_authentication_mechanism_enabled("MONGODB-X509", active_auth_mechanism=False)
        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
        tester.assert_authentication_enabled(expected_num_deployment_auth_mechanisms=2)

        tester.assert_expected_users(1)


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
        users = ac["auth"]["usersWanted"]
        return "CN=x509-testing-user" in [user["user"] for user in users]


@pytest.mark.e2e_replica_set_scram_sha_and_x509
class TestX509CertCreationAndApproval(KubernetesTester):
    def setup_method(self):
        super().setup_method()
        self.cert_file = tempfile.NamedTemporaryFile(delete=False, mode="w")

    def test_create_user_and_authenticate(self, issuer: str, namespace: str, ca_path: str):
        create_x509_user_cert(issuer, namespace, path=self.cert_file.name)
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_x509_authentication(cert_file_name=self.cert_file.name, tlsCAFile=ca_path)

    def teardown(self):
        self.cert_file.close()


@pytest.mark.e2e_replica_set_scram_sha_and_x509
class TestCanStillAuthAsScramUsers(KubernetesTester):
    def test_user_cannot_authenticate_with_incorrect_password(self, ca_path: str):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_scram_sha_authentication_fails(
            password="invalid-password",
            username="mms-user-1",
            ssl=True,
            auth_mechanism="SCRAM-SHA-256",
            tlsCAFile=ca_path,
        )

    def test_user_can_authenticate_with_correct_password(self, ca_path: str):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_scram_sha_authentication(
            password="my-password",
            username="mms-user-1",
            ssl=True,
            auth_mechanism="SCRAM-SHA-256",
            tlsCAFile=ca_path,
        )
