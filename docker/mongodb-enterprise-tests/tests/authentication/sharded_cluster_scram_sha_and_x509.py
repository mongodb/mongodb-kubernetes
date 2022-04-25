import pytest

from kubetester import create_secret
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb_user import MongoDBUser
from kubetester.mongotester import ShardedClusterTester
from kubetester.mongodb import MongoDB, Phase
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs import (
    create_x509_agent_tls_certs,
    create_sharded_cluster_certs,
    create_x509_user_cert,
)

import tempfile

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


@pytest.fixture(scope="module")
def mongodb_user_password_secret(namespace: str) -> str:
    create_secret(
        namespace=namespace,
        name=PASSWORD_SECRET_NAME,
        data={
            "password": USER_PASSWORD,
        },
    )
    return PASSWORD_SECRET_NAME


@pytest.fixture(scope="module")
def scram_user(
    sharded_cluster: MongoDB, mongodb_user_password_secret: str, namespace: str
) -> MongoDBUser:
    user = MongoDBUser.from_yaml(
        load_fixture("scram-sha-user.yaml"), namespace=namespace
    )
    user["spec"]["mongodbResourceRef"]["name"] = sharded_cluster.name
    user["spec"]["passwordSecretKeyRef"]["name"] = mongodb_user_password_secret
    return user.create()


@pytest.fixture(scope="module")
def x509_user(sharded_cluster: MongoDB, namespace: str) -> MongoDBUser:
    user = MongoDBUser.from_yaml(
        load_fixture("test-x509-user.yaml"), namespace=namespace
    )
    user["spec"]["mongodbResourceRef"]["name"] = sharded_cluster.name
    return user.create()


@pytest.mark.e2e_sharded_cluster_scram_sha_and_x509
def test_sharded_cluster_running(sharded_cluster: MongoDB):
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=400)


@pytest.mark.e2e_sharded_cluster_scram_sha_and_x509
def test_sharded_cluster_connectivity(sharded_cluster: MongoDB, ca_path: str):
    tester = sharded_cluster.tester(use_ssl=True, ca_path=ca_path)
    tester.assert_connectivity()


@pytest.mark.e2e_sharded_cluster_scram_sha_and_x509
def test_ops_manager_state_correctly_updated():
    tester = AutomationConfigTester(KubernetesTester.get_automation_config())
    tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
    tester.assert_authentication_enabled()


@pytest.mark.e2e_sharded_cluster_scram_sha_and_x509
def test_user_reaches_updated_phase(scram_user: MongoDBUser):
    scram_user.assert_reaches_phase(Phase.Updated, timeout=150)


@pytest.mark.e2e_sharded_cluster_scram_sha_and_x509
def test_user_cannot_authenticate_with_incorrect_password(ca_path: str):
    tester = ShardedClusterTester(MDB_RESOURCE, 2)
    tester.assert_scram_sha_authentication_fails(
        password="invalid-password",
        username="mms-user-1",
        ssl=True,
        auth_mechanism="SCRAM-SHA-256",
        ssl_ca_certs=ca_path,
    )


@pytest.mark.e2e_sharded_cluster_scram_sha_and_x509
def test_user_can_authenticate_with_correct_password(ca_path: str):
    tester = ShardedClusterTester(MDB_RESOURCE, 2)
    tester.assert_scram_sha_authentication(
        password="my-password",
        username="mms-user-1",
        ssl=True,
        auth_mechanism="SCRAM-SHA-256",
        ssl_ca_certs=ca_path,
    )


@pytest.mark.e2e_sharded_cluster_scram_sha_and_x509
def test_enable_x509(sharded_cluster: MongoDB):
    sharded_cluster.load()
    sharded_cluster["spec"]["security"]["authentication"]["modes"].append("X509")
    sharded_cluster["spec"]["security"]["authentication"]["agents"] = {"mode": "SCRAM"}
    sharded_cluster.update()
    sharded_cluster.assert_abandons_phase(Phase.Running, timeout=50)
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_sharded_cluster_scram_sha_and_x509
def test_ops_manager_state_correctly_updated():
    tester = AutomationConfigTester(KubernetesTester.get_automation_config())
    tester.assert_authentication_mechanism_enabled(
        "MONGODB-X509", active_auth_mechanism=False
    )
    tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
    tester.assert_authentication_enabled(expected_num_deployment_auth_mechanisms=2)
    tester.assert_expected_users(1)


@pytest.mark.e2e_sharded_cluster_scram_sha_and_x509
def test_x509_user_exists_in_automation_config(x509_user: MongoDBUser):
    ac = KubernetesTester.get_automation_config()
    users = ac["auth"]["usersWanted"]
    return x509_user["spec"]["username"] in (user["user"] for user in users)


@pytest.mark.e2e_sharded_cluster_scram_sha_and_x509
class TestX509CertCreationAndApproval(KubernetesTester):
    def setup(self):
        self.cert_file = tempfile.NamedTemporaryFile(delete=False, mode="w")

    def test_create_user_and_authenticate(
        self, issuer: str, namespace: str, ca_path: str
    ):
        create_x509_user_cert(issuer, namespace, path=self.cert_file.name)
        tester = ShardedClusterTester(MDB_RESOURCE, 2)
        tester.assert_x509_authentication(
            cert_file_name=self.cert_file.name, ssl_ca_certs=ca_path
        )


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
