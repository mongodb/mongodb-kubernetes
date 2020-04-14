import pytest

from cryptography import x509
from cryptography.x509.oid import NameOID
from cryptography.hazmat.backends import default_backend

from kubetester.kubetester import KubernetesTester

MDB_RESOURCE = "test-x509-rs"
NUM_AGENTS = 2


def get_cert_names(namespace, members=3, with_agent_certs=False):
    cert_names = [f"{MDB_RESOURCE}-{i}.{namespace}" for i in range(members)]
    if with_agent_certs:
        cert_names += [
            f"mms-monitoring-agent.{namespace}",
            f"mms-backup-agent.{namespace}",
            f"mms-automation-agent.{namespace}",
        ]
    return cert_names


def get_subjects(start, end):
    return [
        f"CN=mms-user-{i},OU=cloud,O=MongoDB,L=New York,ST=New York,C=US"
        for i in range(start, end)
    ]


def get_names_from_certificate_attributes(cert):
    names = {}
    subject = cert.subject
    names["O"] = subject.get_attributes_for_oid(NameOID.ORGANIZATION_NAME)[0].value
    names["OU"] = subject.get_attributes_for_oid(NameOID.ORGANIZATIONAL_UNIT_NAME)[
        0
    ].value
    names["CN"] = subject.get_attributes_for_oid(NameOID.COMMON_NAME)[0].value

    return names


@pytest.mark.e2e_tls_x509_users_addition_removal
class TestReplicaSetUpgradeToTLSWithX509Project(KubernetesTester):
    """
    create:
      file: test-x509-rs.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
      timeout: 240
    """

    def test_mdb_resource_status_is_correct(self):
        for cert in self.yield_existing_csrs(
            get_cert_names(self.namespace, with_agent_certs=True)
        ):
            self.approve_certificate(cert)
        KubernetesTester.wait_until("in_running_state")

    def test_certificates_have_sane_subject(self, namespace):
        agent_certs = KubernetesTester.read_secret(namespace, "agent-certs")
        agent_names = [
            "mms-{}-agent".format(name)
            for name in ["automation", "monitoring", "backup"]
        ]

        for agent in agent_names:
            bytecert = bytearray(agent_certs[agent + "-pem"], "utf-8")
            cert = x509.load_pem_x509_certificate(bytecert, default_backend())
            names = get_names_from_certificate_attributes(cert)

            assert names["CN"] == agent
            assert names["O"] == "cluster.local-agent"
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
        return len(ac["auth"]["usersWanted"]) == 8  # 2 agents + 6 MongoDBUsers

    def test_users_are_added_to_automation_config(self):
        ac = KubernetesTester.get_automation_config()
        existing_users = sorted(
            ac["auth"]["usersWanted"], key=lambda user: user["user"]
        )
        expected_users = sorted(get_subjects(1, 7))
        existing_subjects = [u["user"] for u in ac["auth"]["usersWanted"]]

        for expected in expected_users:
            assert expected in existing_subjects

    def test_automation_users_are_correct(self):
        ac = KubernetesTester.get_automation_config()
        backup_names = get_user_pkix_names(ac, "mms-backup-agent")
        assert backup_names["O"] == "cluster.local-agent"
        assert backup_names["OU"] == KubernetesTester.get_namespace()

        monitoring_names = get_user_pkix_names(ac, "mms-monitoring-agent")
        assert monitoring_names["O"] == "cluster.local-agent"
        assert monitoring_names["OU"] == KubernetesTester.get_namespace()


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
        return len(ac["auth"]["usersWanted"]) == 7  # One user has been deleted

    def test_deleted_user_is_gone(self):
        ac = KubernetesTester.get_automation_config()
        users = ac["auth"]["usersWanted"]
        assert "CN=mms-user-4,OU=cloud,O=MongoDB,L=New York,ST=New York,C=US" not in [
            user["user"] for user in users
        ]


def get_user_pkix_names(ac, agent_name: str) -> str:
    subject = [u["user"] for u in ac["auth"]["usersWanted"] if agent_name in u["user"]][
        0
    ]
    return dict(name.split("=") for name in subject.split(","))
