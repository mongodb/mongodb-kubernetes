import pytest

from kubetester.omtester import get_sc_cert_names
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester
from kubetester.mongotester import ShardedClusterTester

from kubetester.mongodb import MongoDB, Phase
from pytest import fixture

MDB_RESOURCE = "sharded-cluster-x509-to-scram-256"
USER_NAME = "mms-user-1"
PASSWORD_SECRET_NAME = "mms-user-1-password"
USER_PASSWORD = "my-password"


@fixture(scope="module")
def sharded_cluster(namespace: str) -> MongoDB:
    return MongoDB(MDB_RESOURCE, namespace=namespace).load()


@pytest.mark.e2e_sharded_cluster_x509_to_scram_transition
class TestEnableX509ForShardedCluster(KubernetesTester):
    """
    description: |
      Creates a Sharded Cluster with X509 authentication enabled
    create:
      file: sharded-cluster-x509-to-scram-256.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
      timeout: 120
    """

    def test_approve_certs(self):
        for cert in self.yield_existing_csrs(
            get_sc_cert_names(
                MDB_RESOURCE,
                self.namespace,
                with_agent_certs=True,
                members=2,
                config_members=2,
                num_mongos=1,
            )
        ):
            print("Approving certificate {}".format(cert))
            self.approve_certificate(cert)
        KubernetesTester.wait_until("in_running_state")

    def test_ops_manager_state_updated_correctly(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_enabled("MONGODB-X509")
        tester.assert_authentication_enabled()


@pytest.mark.e2e_sharded_cluster_x509_to_scram_transition
def test_enable_scram_and_x509(sharded_cluster: MongoDB):
    sharded_cluster.load()
    sharded_cluster["spec"]["security"]["authentication"]["modes"] = ["X509", "SCRAM"]
    sharded_cluster.update()
    sharded_cluster.assert_abandons_phase(Phase.Running)
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_sharded_cluster_x509_to_scram_transition
def test_x509_is_still_configured():
    tester = AutomationConfigTester(KubernetesTester.get_automation_config())
    tester.assert_authentication_mechanism_enabled("MONGODB-X509")
    tester.assert_authentication_mechanism_enabled(
        "SCRAM-SHA-256", active_auth_mechanism=False
    )
    tester.assert_authentication_enabled(expected_num_deployment_auth_mechanisms=2)


@pytest.mark.e2e_sharded_cluster_x509_to_scram_transition
class TestShardedClusterDisableAuthentication(KubernetesTester):
    def test_disable_auth(self, sharded_cluster: MongoDB):
        sharded_cluster.load()
        sharded_cluster["spec"]["security"]["authentication"]["enabled"] = False
        sharded_cluster.update()
        sharded_cluster.assert_abandons_phase(Phase.Running)
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=900)

    def test_assert_connectivity(self):
        ShardedClusterTester(MDB_RESOURCE, 1, ssl=True).assert_connectivity()

    def test_ops_manager_state_updated_correctly(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_disabled("MONGODB-X509")
        tester.assert_authentication_disabled()


@pytest.mark.e2e_sharded_cluster_x509_to_scram_transition
class TestCanEnableScramSha256:
    def test_can_enable_scram_sha_256(self, sharded_cluster: MongoDB):
        sharded_cluster.load()
        sharded_cluster["spec"]["security"]["authentication"]["enabled"] = True
        sharded_cluster["spec"]["security"]["authentication"]["modes"] = [
            "SCRAM",
        ]
        sharded_cluster["spec"]["security"]["authentication"]["agents"][
            "mode"
        ] = "SCRAM"
        sharded_cluster.update()
        sharded_cluster.assert_abandons_phase(Phase.Running, timeout=100)
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1200)

    def test_assert_connectivity(self):
        ShardedClusterTester(MDB_RESOURCE, 1, ssl=True).assert_connectivity(attempts=25)

    def test_ops_manager_state_updated_correctly(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_disabled("MONGODB-X509")
        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
        tester.assert_authentication_enabled()


@pytest.mark.e2e_sharded_cluster_x509_to_scram_transition
class TestCreateScramSha256User(KubernetesTester):
    """
    description: |
      Creates a SCRAM-SHA-256 user
    create:
      file: scram-sha-user.yaml
      patch: '[{"op":"replace","path":"/spec/mongodbResourceRef/name","value": "sharded-cluster-x509-to-scram-256" }]'
      wait_until: in_updated_state
      timeout: 150
    """

    @classmethod
    def setup_class(cls):
        print(
            f"creating password for MongoDBUser {USER_NAME} in secret/{PASSWORD_SECRET_NAME} "
        )
        KubernetesTester.create_secret(
            KubernetesTester.get_namespace(),
            PASSWORD_SECRET_NAME,
            {
                "password": USER_PASSWORD,
            },
        )
        super().setup_class()

    def test_user_can_authenticate_with_incorrect_password(self):
        tester = ShardedClusterTester(MDB_RESOURCE, 1)
        tester.assert_scram_sha_authentication_fails(
            password="invalid-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-256",
            ssl=True,
        )

    def test_user_can_authenticate_with_correct_password(self):
        tester = ShardedClusterTester(MDB_RESOURCE, 1)
        tester.assert_scram_sha_authentication(
            password="my-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-256",
            ssl=True,
        )


@pytest.mark.e2e_sharded_cluster_x509_to_scram_transition
class TestShardedClusterDeleted(KubernetesTester):
    """
    description: |
      Deletes the Sharded Cluster
    delete:
      file: sharded-cluster-x509-to-scram-256.yaml
      wait_until: mongo_resource_deleted
      timeout: 240
    """

    def test_noop(self):
        pass
