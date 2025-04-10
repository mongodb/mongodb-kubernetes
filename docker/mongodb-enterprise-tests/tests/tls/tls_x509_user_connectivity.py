import tempfile

import pytest
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs import (
    create_agent_tls_certs,
    create_mongodb_tls_certs,
    create_x509_user_cert,
)
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as _fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongotester import ReplicaSetTester
from kubetester.operator import Operator

MDB_RESOURCE = "test-x509-rs"
X509_AGENT_SUBJECT = "CN=automation,OU={namespace},O=cert-manager"


@pytest.fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_agent_tls_certs(issuer, namespace, MDB_RESOURCE)


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str) -> str:
    return create_mongodb_tls_certs(issuer, namespace, MDB_RESOURCE, MDB_RESOURCE + "-cert")


@pytest.fixture(scope="module")
def replica_set(namespace, agent_certs, server_certs, issuer_ca_configmap):
    _ = server_certs
    res = MongoDB.from_yaml(_fixture("test-x509-rs.yaml"), namespace=namespace)
    res["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap

    return res.create()


@pytest.mark.e2e_tls_x509_user_connectivity
def test_install_operator(operator: Operator):
    operator.assert_is_running()


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

            assert "OU" in names
            assert "CN" in names


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
    def setup_method(self):
        super().setup_method()
        self.cert_file = tempfile.NamedTemporaryFile(delete=False, mode="w")

    def test_create_user_and_authenticate(self, issuer: str, namespace: str, ca_path: str):
        create_x509_user_cert(issuer, namespace, path=self.cert_file.name)
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_x509_authentication(cert_file_name=self.cert_file.name, tlsCAFile=ca_path)

    def teardown(self):
        self.cert_file.close()


@pytest.mark.e2e_tls_x509_user_connectivity
class TestX509CorrectlyConfigured(KubernetesTester):
    def test_om_state_is_correct(self, namespace):
        automation_config = KubernetesTester.get_automation_config()
        tester = AutomationConfigTester(automation_config)

        tester.assert_authentication_mechanism_enabled("MONGODB-X509")
        tester.assert_authoritative_set(True)
        tester.assert_expected_users(1)

        user = automation_config["auth"]["autoUser"]
        names = dict(name.split("=") for name in user.split(","))

        assert "OU" in names
        assert "CN=mms-automation-agent" in user
