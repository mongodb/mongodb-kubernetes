import time

import pytest
from kubetester import try_load
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs import (
    ISSUER_CA_NAME,
    create_agent_tls_certs,
    create_mongodb_tls_certs,
)
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongotester import ReplicaSetTester
from kubetester.omtester import get_rs_cert_names
from pytest import fixture

MDB_RESOURCE = "replica-set-x509-to-scram-256"
USER_NAME = "mms-user-1"
PASSWORD_SECRET_NAME = "mms-user-1-password"
USER_PASSWORD = "my-password"


@fixture(scope="module")
def replica_set(namespace: str, server_certs: str, agent_certs: str, issuer_ca_configmap: str) -> MongoDB:
    res = MongoDB.from_yaml(load_fixture("replica-set-x509-to-scram-256.yaml"), namespace=namespace)
    res["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    try_load(res)
    return res


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    return create_mongodb_tls_certs(ISSUER_CA_NAME, namespace, MDB_RESOURCE, f"{MDB_RESOURCE}-cert")


@pytest.fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_agent_tls_certs(issuer, namespace, MDB_RESOURCE)


@pytest.mark.e2e_replica_set_x509_to_scram_transition
class TestEnableX509ForReplicaSet(KubernetesTester):
    def test_replica_set_running(self, replica_set: MongoDB):
        replica_set.update()
        replica_set.assert_reaches_phase(Phase.Running, timeout=400)

    def test_ops_manager_state_updated_correctly(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_enabled("MONGODB-X509")
        tester.assert_authoritative_set(True)
        tester.assert_authentication_enabled()
        tester.assert_expected_users(0)

    def test_deployment_is_reachable(self, replica_set: MongoDB):
        tester = replica_set.tester()
        # Due to what we found out in
        # https://jira.mongodb.org/browse/CLOUDP-68873
        # the agents might report being in goal state, the MDB resource
        # would report no errors but the deployment would be unreachable
        # See the comment inside the function for further details
        time.sleep(20)
        tester.assert_deployment_reachable()


@pytest.mark.e2e_replica_set_x509_to_scram_transition
def test_enable_scram_and_x509(replica_set: MongoDB):
    replica_set.load()
    replica_set["spec"]["security"]["authentication"]["modes"] = ["X509", "SCRAM"]
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_replica_set_x509_to_scram_transition
def test_x509_is_still_configured(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=300)
    tester = AutomationConfigTester(KubernetesTester.get_automation_config())
    tester.assert_authentication_mechanism_enabled("MONGODB-X509")
    tester.assert_authoritative_set(True)
    tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256", active_auth_mechanism=False)
    tester.assert_authentication_enabled(expected_num_deployment_auth_mechanisms=2)
    tester.assert_expected_users(0)


@pytest.mark.e2e_replica_set_x509_to_scram_transition
class TestReplicaSetDisableAuthentication(KubernetesTester):
    def test_disable_auth(self, replica_set: MongoDB):
        replica_set.load()
        replica_set["spec"]["security"]["authentication"]["enabled"] = False
        replica_set.update()
        replica_set.assert_reaches_phase(Phase.Running, timeout=900)

    def test_assert_connectivity(self, replica_set: MongoDB, ca_path: str):
        replica_set.tester(use_ssl=True, ca_path=ca_path).assert_connectivity()

    def test_ops_manager_state_updated_correctly(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_disabled("MONGODB-X509")
        tester.assert_authentication_mechanism_disabled("SCRAM-SHA-256")
        tester.assert_authentication_disabled()


@pytest.mark.e2e_replica_set_x509_to_scram_transition
class TestCanEnableScramSha256:
    def test_can_enable_scram_sha_256(self, replica_set: MongoDB):
        replica_set.load()
        replica_set["spec"]["security"]["authentication"]["enabled"] = True
        replica_set["spec"]["security"]["authentication"]["modes"] = ["SCRAM"]
        replica_set["spec"]["security"]["authentication"]["agents"]["mode"] = "SCRAM"
        replica_set.update()
        replica_set.assert_reaches_phase(Phase.Running, timeout=900)

    def test_assert_connectivity(self, replica_set: MongoDB, ca_path: str):
        replica_set.tester(use_ssl=True, ca_path=ca_path).assert_connectivity()

    def test_ops_manager_state_updated_correctly(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_disabled("MONGODB-X509")
        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
        tester.assert_expected_users(0)
        tester.assert_authentication_enabled()
        tester.assert_authoritative_set(True)


@pytest.mark.e2e_replica_set_x509_to_scram_transition
class TestCreateScramSha256User(KubernetesTester):
    """
    description: |
      Creates a SCRAM-SHA-256 user
    create:
      file: scram-sha-user.yaml
      patch: '[{"op":"replace","path":"/spec/mongodbResourceRef/name","value": "replica-set-x509-to-scram-256" }]'
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

    def test_user_can_authenticate_with_correct_password(self, ca_path: str):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_scram_sha_authentication(
            password="my-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-256",
            ssl=True,
            tlsCAFile=ca_path,
            # As of today, user CRs don't have the status/phase fields. So there's no other way
            # to verify that they were created other than just spinning and checking.
            # See https://jira.mongodb.org/browse/CLOUDP-150729
            # 120 * 5s ~= 600s - the usual timeout we use
            attempts=120,
        )

    def test_user_cannot_authenticate_with_incorrect_password(self, ca_path: str):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_scram_sha_authentication_fails(
            password="invalid-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-256",
            ssl=True,
            tlsCAFile=ca_path,
        )


@pytest.mark.e2e_replica_set_x509_to_scram_transition
class TestReplicaSetDeleted(KubernetesTester):
    """
    description: |
      Deletes the Replica Set
    delete:
      file: replica-set-x509-to-scram-256.yaml
      wait_until: mongo_resource_deleted
      timeout: 240
    """

    def test_noop(self):
        pass
