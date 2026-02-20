import subprocess

import pytest
from pymongo import MongoClient
from pymongo.errors import OperationFailure
from kubetester import kubetester, try_load, read_secret
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs import (
    ISSUER_CA_NAME,
    create_mongodb_tls_certs,
    create_sharded_cluster_certs,
    create_x509_agent_tls_certs,
)
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongotester import ShardedClusterTester
from kubetester.omtester import get_sc_cert_names
from kubetester.phase import Phase
from opentelemetry import trace
from pytest import fixture
from tests import test_logger

TRACER = trace.get_tracer("evergreen-agent")

MDB_RESOURCE = "sharded-cluster-x509-to-scram-256"
USER_NAME = "mms-user-1"
PASSWORD_SECRET_NAME = "mms-user-1-password"
USER_PASSWORD = "my-password"
logger = test_logger.get_test_logger(__name__)


@fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_x509_agent_tls_certs(issuer, namespace, MDB_RESOURCE)


@fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    create_sharded_cluster_certs(
        namespace,
        MDB_RESOURCE,
        shards=2,
        mongod_per_shard=3,
        config_servers=2,
        mongos=1,
    )


@fixture(scope="module")
def sharded_cluster(namespace: str, server_certs: str, agent_certs: str, issuer_ca_configmap: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        load_fixture("sharded-cluster-x509-to-scram-256.yaml"),
        namespace=namespace,
    )
    resource["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    try_load(resource)
    return resource


def _create_automation_agent_user(namespace: str, resource_name: str, ca_path: str):
    """
    CLOUDP-383102 workaround: Pre-create the automation agent user before enabling SCRAM.

    When transitioning from disabled-auth → SCRAM-only, the keyfile on disk from the
    previous auth phase blocks the localhost exception for external connections. This
    prevents the automation agent from bootstrapping its SCRAM user, causing a deadlock.

    It's racy - sometimes it works, sometimes not. It depends whether the agent first
    creates the user or first restarts mongos.

    Uses pymongo to check if user exists (works from anywhere), uses kubectl exec
    to create the user (requires localhost exception inside the container).
    """
    # Get the automation agent password from the K8s secret
    password = read_secret(namespace, f"{resource_name}-agent-auth-secret")["automation-agent-password"]

    # Use short hostname (works with kubefwdc /etc/hosts)
    config_server_host = f"{resource_name}-config-0.{resource_name}-cs:27017"

    # First, check if the user already exists by trying to authenticate via pymongo
    try:
        client = MongoClient(
            config_server_host,
            username="mms-automation-agent",
            password=password,
            authSource="admin",
            tls=True,
            tlsCAFile=ca_path,
            tlsAllowInvalidHostnames=True,
            directConnection=True,
            serverSelectionTimeoutMS=5000,
        )
        client.admin.command("ping")
        client.close()
        logger.info("mms-automation-agent user already exists")
        return
    except OperationFailure as e:
        if e.code == 18:
            logger.info("mms-automation-agent user does not exist, will create")
        else:
            logger.warning(f"Unexpected error checking user: {e}")
    except Exception as e:
        logger.info(f"Could not verify user exists ({e}), will try to create")

    # User doesn't exist - create via kubectl exec (needs localhost exception)
    config_pod = f"{resource_name}-config-0"
    create_user_js = f"""
db.createUser({{
  user: 'mms-automation-agent',
  pwd: '{password}',
  roles: ['backup','clusterAdmin','dbAdminAnyDatabase','readWriteAnyDatabase','restore','userAdminAnyDatabase'].map(r=>({{role:r,db:'admin'}})),
  mechanisms: ['SCRAM-SHA-256']
}})
"""

    cmd = [
        "kubectl", "exec", "-n", namespace, config_pod,
        "-c", "mongodb-enterprise-database", "--",
        "env", "HOME=/tmp", "/usr/bin/mongosh",
        "--tls",
        "--tlsCAFile", "/mongodb-automation/tls/ca/ca-pem",
        "--tlsAllowInvalidHostnames",
        "--norc",
        "mongodb://localhost:27017/admin",
        "--eval", create_user_js,
    ]

    try:
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=30)
        if result.returncode == 0:
            logger.info("Pre-created mms-automation-agent user on config server")
        elif "already exists" in result.stderr or "already exists" in result.stdout:
            logger.info("mms-automation-agent user already exists, skipping")
        elif "requires authentication" in result.stderr or "requires authentication" in result.stdout:
            # Localhost exception not available (other users exist)
            logger.info("Localhost exception unavailable, assuming user exists from previous run")
        else:
            logger.warning(f"Failed to create user: {result.stderr or result.stdout}")
    except subprocess.TimeoutExpired:
        logger.warning("Timed out creating automation agent user")
    except Exception as e:
        logger.warning(f"Error creating automation agent user: {e}")


@pytest.mark.e2e_sharded_cluster_x509_to_scram_transition
class TestEnableX509ForShardedCluster(KubernetesTester):
    def test_create_resource(self, sharded_cluster: MongoDB):
        sharded_cluster.update()
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1200)

    def test_ops_manager_state_updated_correctly(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_enabled("MONGODB-X509")
        tester.assert_authentication_enabled()


@pytest.mark.e2e_sharded_cluster_x509_to_scram_transition
def test_enable_scram_and_x509(sharded_cluster: MongoDB):
    sharded_cluster.load()
    sharded_cluster["spec"]["security"]["authentication"]["modes"] = ["X509", "SCRAM"]
    sharded_cluster.update()
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_sharded_cluster_x509_to_scram_transition
def test_x509_is_still_configured():
    tester = AutomationConfigTester(KubernetesTester.get_automation_config())
    tester.assert_authentication_mechanism_enabled("MONGODB-X509")
    tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256", active_auth_mechanism=False)
    tester.assert_authentication_enabled(expected_num_deployment_auth_mechanisms=2)


@pytest.mark.e2e_sharded_cluster_x509_to_scram_transition
class TestShardedClusterDisableAuthentication(KubernetesTester):
    def test_disable_auth(self, sharded_cluster: MongoDB):
        kubetester.wait_processes_ready()
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=800)
        sharded_cluster.load()
        sharded_cluster["spec"]["security"]["authentication"]["enabled"] = False
        sharded_cluster.update()
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1500)

    def test_assert_connectivity(self, ca_path: str):
        ShardedClusterTester(MDB_RESOURCE, 1, ssl=True, ca_path=ca_path).assert_connectivity()

    def test_ops_manager_state_updated_correctly(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_disabled("MONGODB-X509")
        tester.assert_authentication_disabled()


@pytest.mark.e2e_sharded_cluster_x509_to_scram_transition
class TestCanEnableScramSha256:
    @TRACER.start_as_current_span("test_can_enable_scram_sha_256")
    def test_can_enable_scram_sha_256(self, sharded_cluster: MongoDB, ca_path: str, namespace: str):
        kubetester.wait_processes_ready()
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1400)

        sharded_cluster.load()
        sharded_cluster["spec"]["security"]["authentication"]["enabled"] = True
        sharded_cluster["spec"]["security"]["authentication"]["modes"] = [
            "SCRAM",
        ]
        sharded_cluster["spec"]["security"]["authentication"]["agents"]["mode"] = "SCRAM"
        sharded_cluster.update()

        # Try to reach Running phase with initial timeout
        try:
            sharded_cluster.assert_reaches_phase(Phase.Running, timeout=480)
        except Exception as e:
            # CLOUDP-383102: If it times out, create the automation agent user and retry.
            # This works around a race condition where the agent restarts mongos with authOn
            # before creating the mms-automation-agent user on config servers.
            logger.warning(
                f"CLOUDP-383102: Initial wait timed out after 480s. "
                f"Pre-creating automation agent user via localhost exception. Error: {e}"
            )
            with TRACER.start_as_current_span("cloudp_383102_workaround") as span:
                span.set_attribute("workaround.ticket", "CLOUDP-383102")
                span.set_attribute("workaround.reason", "auth_transition_timeout")
                span.set_attribute("workaround.action", "precreate_automation_agent_user")
                _create_automation_agent_user(namespace, MDB_RESOURCE, ca_path)
            sharded_cluster.assert_reaches_phase(Phase.Running, timeout=600)

    def test_assert_connectivity(self, ca_path: str):
        ShardedClusterTester(MDB_RESOURCE, 1, ssl=True, ca_path=ca_path).assert_connectivity(attempts=25)

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
        print(f"creating password for MongoDBUser {USER_NAME} in secret/{PASSWORD_SECRET_NAME} ")
        KubernetesTester.create_secret(
            KubernetesTester.get_namespace(),
            PASSWORD_SECRET_NAME,
            {
                "password": USER_PASSWORD,
            },
        )
        super().setup_class()

    def test_user_can_authenticate_with_incorrect_password(self, ca_path: str):
        tester = ShardedClusterTester(MDB_RESOURCE, 1)
        tester.assert_scram_sha_authentication_fails(
            password="invalid-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-256",
            ssl=True,
            tlsCAFile=ca_path,
        )

    def test_user_can_authenticate_with_correct_password(self, ca_path: str):
        tester = ShardedClusterTester(MDB_RESOURCE, 1)
        tester.assert_scram_sha_authentication(
            password="my-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-256",
            ssl=True,
            tlsCAFile=ca_path,
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
