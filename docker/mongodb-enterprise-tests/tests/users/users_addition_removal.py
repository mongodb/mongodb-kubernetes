import pytest

from kubetester.kubetester import KubernetesTester

MDB_RESOURCE = "test-x509-rs"
NUM_AGENTS = 2


def get_cert_names(namespace, members=3, with_agent_certs=False):
    cert_names = [f"{MDB_RESOURCE}-{i}.{namespace}" for i in range(members)]
    if with_agent_certs:
        cert_names += [
            f'mms-monitoring-agent.{namespace}',
            f'mms-backup-agent.{namespace}',
            f'mms-automation-agent.{namespace}'
        ]
    return cert_names


def get_subjects(start, end):
    subjects = [f'CN=mms-user-{i},OU=cloud,O=MongoDB,L=New York,ST=New York,C=US' for i in range(start, end)]
    subjects.append("CN=mms-backup-agent,OU=MongoDB Kubernetes Operator,O=mms-backup-agent,L=NY,ST=NY,C=US")
    subjects.append("CN=mms-monitoring-agent,OU=MongoDB Kubernetes Operator,O=mms-monitoring-agent,L=NY,ST=NY,C=US")
    return subjects


@pytest.mark.e2e_tls_x509_users_addition_removal
class TestReplicaSetUpgradeToTLSWithX509Project(KubernetesTester):
    """
    create:
      file: test-x509-rs.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
      timeout: 240
    """

    def test_mdb_resource_status_is_correct(self):
        for cert in self.yield_existing_csrs(get_cert_names(self.namespace, with_agent_certs=True)):
            self.approve_certificate(cert)
        KubernetesTester.wait_until('in_running_state')


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
        return len(ac['auth']['usersWanted']) == 8  # 2 agents + 6 MongoDBUsers

    def test_users_are_added_to_automation_config(self):
        ac = KubernetesTester.get_automation_config()
        users = sorted(ac['auth']['usersWanted'], key=lambda user: user['user'])
        subjects = sorted(get_subjects(1, 7))

        assert len(users) == len(subjects)
        for expected, user in zip(subjects, users):
            assert user['user'] == expected
            assert user['db'] == '$external'


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
        return len(ac['auth']['usersWanted']) == 7  # One user has been deleted

    def test_deleted_user_is_gone(self):
        ac = KubernetesTester.get_automation_config()
        users = ac['auth']['usersWanted']
        assert 'CN=mms-user-4,OU=cloud,O=MongoDB,L=New York,ST=New York,C=US' not in [user['user'] for user in users]
