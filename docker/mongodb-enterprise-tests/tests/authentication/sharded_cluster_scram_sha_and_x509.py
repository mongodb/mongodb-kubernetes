import pytest

from kubetester.kubetester import KubernetesTester
from kubetester.mongotester import ShardedClusterTester
from kubetester.omtester import get_sc_cert_names
from kubetester.mongodb import MongoDB, Phase
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs import (
    Certificate,
    ISSUER_CA_NAME,
    create_mongodb_tls_certs,
    create_x509_agent_tls_certs,
    create_sharded_cluster_certs,
)
from kubetester.kubetester import fixture as load_fixture

MDB_RESOURCE = "sharded-cluster-tls-scram-sha-256"
USER_NAME = "mms-user-1"
PASSWORD_SECRET_NAME = "mms-user-1-password"
USER_PASSWORD = "my-password"


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    create_sharded_cluster_certs(
        namespace,
        MDB_RESOURCE,
        shards=1,
        mongos_per_shard=3,
        config_servers=3,
        mongos=2,
    )


@pytest.fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_x509_agent_tls_certs(issuer, namespace, MDB_RESOURCE)


@pytest.fixture(scope="module")
def sharded_cluster(
    namespace: str, server_certs, agent_certs: str, issuer_ca_configmap: str
) -> MongoDB:
    res = MongoDB.from_yaml(
        load_fixture("sharded-cluster-tls-scram-sha-256.yaml"), namespace=namespace
    )
    res["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    return res.create()


@pytest.mark.e2e_sharded_cluster_scram_sha_and_x509
class TestShardedClusterCreation(KubernetesTester):
    def test_sharded_cluster_running(self, sharded_cluster: MongoDB):
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=400)

    def test_sharded_cluster_connectivity(self, sharded_cluster: MongoDB, ca_path: str):
        tester = sharded_cluster.tester(use_ssl=True, ca_path=ca_path)
        tester.assert_connectivity()

    def test_ops_manager_state_correctly_updated(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
        tester.assert_authentication_enabled()


@pytest.mark.e2e_sharded_cluster_scram_sha_and_x509
class TestCreateMongoDBUser(KubernetesTester):
    """
    description: |
      Creates a MongoDBUser
    create:
      file: scram-sha-user.yaml
      patch: '[{"op":"replace","path":"/spec/mongodbResourceRef/name","value": "sharded-cluster-tls-scram-sha-256" }]'
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

    def test_create_user(self):
        pass


@pytest.mark.e2e_sharded_cluster_scram_sha_and_x509
class TestScramUserCanAuthenticate(KubernetesTester):
    def test_user_cannot_authenticate_with_incorrect_password(self, ca_path: str):
        tester = ShardedClusterTester(MDB_RESOURCE, 2)
        tester.assert_scram_sha_authentication_fails(
            password="invalid-password",
            username="mms-user-1",
            ssl=True,
            auth_mechanism="SCRAM-SHA-256",
            ssl_ca_certs=ca_path,
        )

    def test_user_can_authenticate_with_correct_password(self, ca_path: str):
        tester = ShardedClusterTester(MDB_RESOURCE, 2)
        tester.assert_scram_sha_authentication(
            password="my-password",
            username="mms-user-1",
            ssl=True,
            auth_mechanism="SCRAM-SHA-256",
            ssl_ca_certs=ca_path,
        )


@pytest.mark.e2e_sharded_cluster_scram_sha_and_x509
class TestEnableX509(KubernetesTester):
    def test_enable_x509(self, sharded_cluster: MongoDB):
        sharded_cluster.load()
        sharded_cluster["spec"]["security"]["authentication"]["modes"].append("X509")
        sharded_cluster["spec"]["security"]["authentication"]["agents"] = {
            "mode": "SCRAM"
        }
        sharded_cluster.update()
        sharded_cluster.assert_abandons_phase(Phase.Running, timeout=50)
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=900)

    # important note that no CSRs for the agents should have been created
    def test_ops_manager_state_correctly_updated(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_enabled(
            "MONGODB-X509", active_auth_mechanism=False
        )
        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
        tester.assert_authentication_enabled(expected_num_deployment_auth_mechanisms=2)
        tester.assert_expected_users(3)


@pytest.mark.e2e_sharded_cluster_scram_sha_and_x509
class TestAddMongoDBUser(KubernetesTester):
    """
    create:
      file: test-x509-user.yaml
      patch: '[{"op":"replace","path":"/spec/mongodbResourceRef/name","value": "sharded-cluster-tls-scram-sha-256" }]'
      wait_until: user_exists
    """

    def test_add_user(self):
        assert True

    @staticmethod
    def user_exists():
        ac = KubernetesTester.get_automation_config()
        users = ac["auth"]["usersWanted"]
        return "CN=x509-testing-user" in [user["user"] for user in users]


@pytest.mark.e2e_sharded_cluster_scram_sha_and_x509
class TestX509CertCreationAndApproval(KubernetesTester):
    def setup(self):
        cert_name = "x509-testing-user." + self.get_namespace()
        self.cert_file = self.generate_certfile(
            cert_name, "x509-testing-user.csr", "server-key.pem"
        )

    def teardown(self):
        self.cert_file.close()

    def test_can_authenticate_with_added_user(self):
        tester = ShardedClusterTester(MDB_RESOURCE, 2)
        tester.assert_x509_authentication(cert_file_name=self.cert_file.name)


@pytest.mark.e2e_sharded_cluster_scram_sha_and_x509
class TestCanStillAuthAsScramUsers(KubernetesTester):
    def test_user_cannot_authenticate_with_incorrect_password(self, ca_path: str):
        tester = ShardedClusterTester(MDB_RESOURCE, 2)
        tester.assert_scram_sha_authentication_fails(
            password="invalid-password",
            username="mms-user-1",
            ssl=True,
            auth_mechanism="SCRAM-SHA-256",
            ssl_ca_certs=ca_path,
        )

    def test_user_can_authenticate_with_correct_password(self, ca_path: str):
        tester = ShardedClusterTester(MDB_RESOURCE, 2)
        tester.assert_scram_sha_authentication(
            password="my-password",
            username="mms-user-1",
            ssl=True,
            auth_mechanism="SCRAM-SHA-256",
            ssl_ca_certs=ca_path,
        )
