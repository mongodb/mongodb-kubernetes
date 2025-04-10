import pytest
from cryptography import x509
from cryptography.hazmat.backends import default_backend
from cryptography.x509.oid import NameOID
from kubetester import find_fixture
from kubetester.certs import (
    ISSUER_CA_NAME,
    Certificate,
    create_agent_tls_certs,
    create_mongodb_tls_certs,
)
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB, Phase

MDB_RESOURCE = "test-x509-rs"
NUM_AGENTS = 2


def get_subjects(start, end):
    return [f"CN=mms-user-{i},OU=cloud,O=MongoDB,L=New York,ST=New York,C=US" for i in range(start, end)]


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    return create_mongodb_tls_certs(ISSUER_CA_NAME, namespace, MDB_RESOURCE, f"{MDB_RESOURCE}-cert")


def get_names_from_certificate_attributes(cert):
    names = {}
    subject = cert.subject
    names["OU"] = subject.get_attributes_for_oid(NameOID.ORGANIZATIONAL_UNIT_NAME)[0].value
    names["CN"] = subject.get_attributes_for_oid(NameOID.COMMON_NAME)[0].value

    return names


@pytest.fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_agent_tls_certs(issuer, namespace, MDB_RESOURCE)


@pytest.fixture(scope="module")
def mdb(namespace: str, server_certs: str, agent_certs: str, issuer_ca_configmap: str) -> MongoDB:
    res = MongoDB.from_yaml(load_fixture("test-x509-rs.yaml"), namespace=namespace)
    res["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    return res.create()


@pytest.mark.e2e_tls_x509_users_addition_removal
class TestReplicaSetUpgradeToTLSWithX509Project(KubernetesTester):
    def test_mdb_resource_running(self, mdb: MongoDB):
        mdb.assert_reaches_phase(Phase.Running, timeout=300)

    def test_certificates_have_sane_subject(self, namespace):
        agent_certs = KubernetesTester.read_secret(namespace, "agent-certs-pem")

        bytecert = bytes(agent_certs["mms-automation-agent-pem"], "utf-8")
        cert = x509.load_pem_x509_certificate(bytecert, default_backend())
        names = get_names_from_certificate_attributes(cert)

        assert names["CN"] == "mms-automation-agent"
        assert names["OU"] == namespace


@pytest.mark.e2e_tls_x509_users_addition_removal
class TestMultipleUsersAreAdded(KubernetesTester):
    """
    name: Test users are added correctly
    create_many:
      file: users_multiple.yaml
      wait_until: all_users_ready
    """

    def test_users_ready(self):
        pass

    @staticmethod
    def all_users_ready():
        ac = KubernetesTester.get_automation_config()
        return len(ac["auth"]["usersWanted"]) == 6  # 6 MongoDBUsers

    def test_users_are_added_to_automation_config(self):
        ac = KubernetesTester.get_automation_config()
        existing_users = sorted(ac["auth"]["usersWanted"], key=lambda user: user["user"])
        expected_users = sorted(get_subjects(1, 7))
        existing_subjects = [u["user"] for u in ac["auth"]["usersWanted"]]

        for expected in expected_users:
            assert expected in existing_subjects


@pytest.mark.e2e_tls_x509_users_addition_removal
class TestTheCorrectUserIsDeleted(KubernetesTester):
    """
    delete:
      delete_name: mms-user-4
      file: users_multiple.yaml
      wait_until: user_has_been_deleted
    """

    @staticmethod
    def user_has_been_deleted():
        ac = KubernetesTester.get_automation_config()
        return len(ac["auth"]["usersWanted"]) == 5  # One user has been deleted

    def test_deleted_user_is_gone(self):
        ac = KubernetesTester.get_automation_config()
        users = ac["auth"]["usersWanted"]
        assert "CN=mms-user-4,OU=cloud,O=MongoDB,L=New York,ST=New York,C=US" not in [user["user"] for user in users]


def get_user_pkix_names(ac, agent_name: str) -> str:
    subject = [u["user"] for u in ac["auth"]["usersWanted"] if agent_name in u["user"]][0]
    return dict(name.split("=") for name in subject.split(","))
