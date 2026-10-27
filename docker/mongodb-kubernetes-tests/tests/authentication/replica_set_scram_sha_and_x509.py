import tempfile
from typing import Dict

import pytest
from kubetester import read_secret, try_load
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs import ISSUER_CA_NAME, create_mongodb_tls_certs, create_x509_user_cert
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoTester, ReplicaSetTester, assert_connection_string_with_mongosh, with_x509
from kubetester.phase import Phase

MDB_RESOURCE = "replica-set-scram-256-and-x509"
USER_NAME = "mms-user-1"
PASSWORD_SECRET_NAME = "mms-user-1-password"
USER_PASSWORD = "my-password"


@pytest.fixture(scope="module")
def replica_set(namespace: str, issuer_ca_configmap: str, server_certs: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("replica-set-tls-scram-sha-256.yaml"), namespace=namespace)
    resource["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    try_load(resource)
    return resource


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    return create_mongodb_tls_certs(ISSUER_CA_NAME, namespace, MDB_RESOURCE, f"{MDB_RESOURCE}-cert")


@pytest.mark.e2e_replica_set_scram_sha_and_x509
class TestReplicaSetCreation(KubernetesTester):
    def test_replica_set_running(self, replica_set: MongoDB):
        replica_set.update()
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
    description: |
      Creates the x509 MongoDBUser. connectionStringDatabase is set to admin, the only database
      this user holds readWrite on, so that a write through the generated URI lands somewhere its
      roles apply.
    create:
      file: test-x509-user.yaml
      patch: '[{"op":"replace","path":"/spec/mongodbResourceRef/name","value": "replica-set-scram-256-and-x509" },{"op":"add","path":"/spec/connectionStringDatabase","value": "admin" }]'
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


# Secret name: {resource}-{user-object-name}-external  ($external strips the $)
X509_USER_SECRET_NAME = f"{MDB_RESOURCE}-test-x509-user-external"


@pytest.mark.e2e_replica_set_scram_sha_and_x509
def test_external_user_connection_string_has_no_scram_mechanism(namespace: str):
    """The resource enables both SCRAM and X509, so the SCRAM mechanism must not leak into
    the connection string of a user authenticating against $external, and no password is set."""
    secret: Dict[str, str] = read_secret(namespace, X509_USER_SECRET_NAME)

    for key in ("connectionString.standard", "connectionString.standardSrv"):
        assert key in secret
        connection_string = secret[key]
        assert "authSource=$external" in connection_string
        assert "authMechanism" not in connection_string
        # No credentials belong in an external user's URI
        assert "@" not in connection_string
        # authSource stays $external while the URI path carries connectionStringDatabase
        assert "/admin?" in connection_string

    assert "password" not in secret


@pytest.mark.e2e_replica_set_scram_sha_and_x509
def test_external_user_connection_string_can_connect(namespace: str, issuer: str, ca_path: str):
    secret: Dict[str, str] = read_secret(namespace, X509_USER_SECRET_NAME)
    cert_file = tempfile.NamedTemporaryFile(delete=False, mode="w")
    try:
        create_x509_user_cert(issuer, namespace, path=cert_file.name)
        x509_opts = with_x509(cert_file.name, ca_path)
        for key in ("connectionString.standard", "connectionString.standardSrv"):
            MongoTester(secret[key], use_ssl=True, ca_path=ca_path).assert_connectivity(opts=[x509_opts])
    finally:
        cert_file.close()


@pytest.mark.e2e_replica_set_scram_sha_and_x509
def test_external_user_connection_string_with_mongosh(namespace: str, issuer: str, ca_path: str):
    """mongosh uses the secret URI as-is, like a customer would. X.509 has to be named explicitly
    because mechanism negotiation only ever selects between the SCRAM mechanisms, so the check is
    that the generated string accepts the mechanism rather than contradicting it.

    The script asserts on the authenticated identity and then writes, because a command such as
    ping needs neither authentication nor any privilege and would pass even when auth failed."""
    secret: Dict[str, str] = read_secret(namespace, X509_USER_SECRET_NAME)
    cert_file = tempfile.NamedTemporaryFile(delete=False, mode="w")
    try:
        create_x509_user_cert(issuer, namespace, path=cert_file.name)
        tls_args = f"tls=true&tlsCertificateKeyFile={cert_file.name}&tlsCAFile={ca_path}"
        authenticate_and_write = (
            "const users = db.runCommand({connectionStatus: 1}).authInfo.authenticatedUsers;"
            "if (!users.some(u => u.user === 'CN=x509-testing-user' && u.db === '$external'))"
            "  throw new Error('not authenticated as the $external user: ' + JSON.stringify(users));"
            "db.mongoshX509Check.insertOne({});"
        )

        for key in ("connectionString.standard", "connectionString.standardSrv"):
            connection_string = secret[key]

            assert_connection_string_with_mongosh(
                f"{connection_string}&authMechanism=MONGODB-X509&{tls_args}",
                expect_success=True,
                eval_script=authenticate_and_write,
            )

            # The mechanism the operator used to write here cannot authenticate an $external user,
            # so the connection is left unauthenticated and the write is refused.
            assert_connection_string_with_mongosh(
                f"{connection_string}&authMechanism=SCRAM-SHA-256&{tls_args}",
                expect_success=False,
                eval_script=authenticate_and_write,
            )
    finally:
        cert_file.close()
