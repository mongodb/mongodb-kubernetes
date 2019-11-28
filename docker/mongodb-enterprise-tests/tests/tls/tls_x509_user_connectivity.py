import pytest
from kubetester.kubetester import KubernetesTester
from kubetester.omtester import get_rs_cert_names
from kubetester.mongotester import ReplicaSetTester
from kubetester.automation_config_tester import (
    AutomationConfigTester,
    X509_AGENT_SUBJECT,
)

MDB_RESOURCE = "test-x509-rs"
BACKUP_AGENT_USER = "CN=mms-backup-agent,OU=MongoDB Kubernetes Operator,O=mms-backup-agent,L=NY,ST=NY,C=US"
MONITORING_AGENT_USER = "CN=mms-monitoring-agent,OU=MongoDB Kubernetes Operator,O=mms-monitoring-agent,L=NY,ST=NY,C=US"


@pytest.mark.e2e_tls_x509_user_connectivity
class TestReplicaSetWithTLSCreation(KubernetesTester):
    """
    name: Replica Set With TLS Creation
    description: |
      Creates a MongoDB object with the ssl attribute on. The MongoDB object will go to Failed
      state because of missing certificates.
    create:
      file: test-x509-rs.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
    """

    def test_approve_certs(self):
        for cert in self.yield_existing_csrs(
            get_rs_cert_names(MDB_RESOURCE, self.get_namespace(), with_agent_certs=True)
        ):
            self.approve_certificate(cert)
        KubernetesTester.wait_until("in_running_state", 320)

    def test_users_wanted_is_correct(self):
        """At this stage we should have 2 members in the usersWanted list,
        monitoring-agent and backup-agent."""

        automation_config = KubernetesTester.get_automation_config()
        tester = AutomationConfigTester(automation_config)

        tester.assert_has_user(BACKUP_AGENT_USER)
        tester.assert_has_user(MONITORING_AGENT_USER)


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
    def test_om_state_is_correct(self):
        tester = AutomationConfigTester(
            KubernetesTester.get_automation_config(), expected_users=3
        )

        tester.assert_authentication_mechanism_enabled("MONGODB-X509")
        tester.assert_agent_user(X509_AGENT_SUBJECT)
