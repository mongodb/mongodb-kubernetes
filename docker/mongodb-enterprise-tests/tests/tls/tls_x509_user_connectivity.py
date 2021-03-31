import pytest
from kubetester.kubetester import KubernetesTester, fixture as _fixture
from kubetester.mongotester import ReplicaSetTester
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs import Certificate, ISSUER_CA_NAME
from kubetester.mongodb import MongoDB, Phase

import copy
import time


MDB_RESOURCE = "test-x509-rs"
X509_AGENT_SUBJECT = "CN=automation,OU={namespace},O=cert-manager"

SUBJECT = {
    # Organizational Units matches your namespace (to be overriden by test)
    "organizationalUnits": ["TO-BE-REPLACED"],
    # For an additional layer of security, the certificates will have a random
    # (unknown and "unpredictable"), random string. Even if someone was able to
    # generate the certificates themselves, they would still require this
    # value to do so.
    "serialNumber": "TO-BE-REPLACED",
}


def generate_cert(namespace, name, usages=None, subject=None, san=None):
    if usages is None:
        usages = ["server auth", "client auth"]

    if subject is None:
        subject = {}

    if san is None:
        san = [name]

    cert = Certificate(namespace=namespace, name=name + "-cert")
    cert["spec"] = {
        "secretName": name + "-cert-secret",
        "issuerRef": {"name": ISSUER_CA_NAME},
        "duration": "240h",
        "renewBefore": "120h",
        "usages": usages,
        "commonName": name,
        "subject": subject,
        "dnsNames": san,
    }
    cert.create().block_until_ready()

    return name + "-cert-secret"


def generate_client_cert(namespace, name):
    subject = copy.deepcopy(SUBJECT)
    subject["serialNumber"] = KubernetesTester.random_k8s_name(prefix="sn-")
    subject["organizationalUnits"] = [namespace]

    return generate_cert(namespace, name, subject=subject, usages=["client auth"])


def generate_server_cert(namespace, name, san):
    return generate_cert(namespace, name, san=san)


@pytest.fixture(scope="module")
def agent_certs(issuer: str, namespace: str):
    """Creates an 'agent-certs' Secret containing client TLS certs for 3 agents."""
    agents = ["automation", "monitoring", "backup"]

    for agent in agents:
        generate_client_cert(namespace, agent)

    time.sleep(10)
    full_certs = {}
    for agent in agents:
        agent_cert = KubernetesTester.read_secret(namespace, agent + "-cert-secret")
        full_certs["mms-{}-agent-pem".format(agent)] = (
            agent_cert["tls.crt"] + agent_cert["tls.key"]
        )

    KubernetesTester.create_secret(namespace, "agent-certs", full_certs)


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    """Creates one 'test-x509-rs-cert' Secret with server TLS certs for 3 RS members. """
    resource_name = "test-x509-rs"
    pod_fqdn_fstring = "{resource_name}-{index}.{resource_name}-svc.{namespace}.svc.cluster.local".format(
        resource_name=resource_name,
        namespace=namespace,
        index="{}",
    )
    data = {}
    for i in range(3):
        pod_dns = pod_fqdn_fstring.format(i)
        pod_name = f"{resource_name}-{i}"
        cert = generate_server_cert(namespace, pod_name, [pod_dns, pod_name])
        secret = KubernetesTester.read_secret(namespace, cert)
        data[pod_name + "-pem"] = secret["tls.key"] + secret["tls.crt"]

    KubernetesTester.create_secret(namespace, f"{resource_name}-cert", data)

    return f"{resource_name}-cert"


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
