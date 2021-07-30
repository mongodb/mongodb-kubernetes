import pytest
from kubetester.kubetester import KubernetesTester, fixture as _fixture
from kubetester.mongotester import ReplicaSetTester
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs import (
    ISSUER_CA_NAME,
    create_mongodb_tls_certs,
    create_agent_tls_certs,
)
from kubetester.mongodb import MongoDB, Phase


MDB_RESOURCE = "test-x509-rs"
X509_AGENT_SUBJECT = "CN=automation,OU={namespace},O=cert-manager"


@pytest.fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_agent_tls_certs(issuer, namespace, MDB_RESOURCE)


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str) -> str:
    return create_mongodb_tls_certs(
        issuer, namespace, MDB_RESOURCE, MDB_RESOURCE + "-cert"
    )


@pytest.fixture(scope="module")
def replica_set(namespace, agent_certs, server_certs, issuer_ca_configmap):
    _ = server_certs
    res = MongoDB.from_yaml(_fixture("test-x509-rs.yaml"), namespace=namespace)
    res["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap

    return res.create()


@pytest.mark.e2e_tls_x509_user_connectivity
class TestReplicaSetWithTLSCreation(KubernetesTester):
    def test_users_wanted_is_correct(self, replica_set, namespace):
        """At this stage we should have 2 members in the usersWanted list,
        monitoring-agent and backup-agent."""

        replica_set.assert_reaches_phase(Phase.Running, timeout=600)

        automation_config = KubernetesTester.get_automation_config()
        users = [u["user"] for u in automation_config["auth"]["usersWanted"]]

        for subject in users:
            names = dict(name.split("=") for name in subject.split(","))

            assert "SERIALNUMBER" in names
            assert "OU" in names
            assert "CN" in names

        # exception with IndexError if not found
        backup = [u for u in users if "CN=backup" in u][0]
        monitoring = [u for u in users if "CN=monitoring" in u][0]


@pytest.mark.e2e_tls_x509_user_connectivity
class TestAddMongoDBUser(KubernetesTester):
    """
    create:
      file: test-x509-user.yaml
      patch: '[{"op":"replace","path":"/spec/mongodbResourceRef/name","value": "test-x509-rs" }]'
      wait_until: user_exists
    """

    def test_add_user(self):
        assert True

    @staticmethod
    def user_exists():
        ac = KubernetesTester.get_automation_config()
        users = ac["auth"]["usersWanted"]

        return "CN=x509-testing-user" in [user["user"] for user in users]


@pytest.mark.e2e_tls_x509_user_connectivity
class TestX509CertCreationAndApproval(KubernetesTester):
    def setup(self):
        cert_name = "x509-testing-user." + self.get_namespace()
        self.cert_file = self.generate_certfile(
            cert_name, "x509-testing-user.csr", "server-key.pem"
        )

    def teardown(self):
        self.cert_file.close()

    def test_can_authenticate_with_added_user(self):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_x509_authentication(self.cert_file.name)


@pytest.mark.e2e_tls_x509_user_connectivity
class TestX509CorrectlyConfigured(KubernetesTester):
    def test_om_state_is_correct(self, namespace):
        automation_config = KubernetesTester.get_automation_config()
        tester = AutomationConfigTester(automation_config)

        tester.assert_authentication_mechanism_enabled("MONGODB-X509")
        tester.assert_authoritative_set(True)
        tester.assert_expected_users(3)

        user = automation_config["auth"]["autoUser"]
        names = dict(name.split("=") for name in user.split(","))

        assert "OU" in names
        assert "CN=automation" in user
