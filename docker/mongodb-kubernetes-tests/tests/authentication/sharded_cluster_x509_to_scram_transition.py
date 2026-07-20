import subprocess

import pytest
from kubetester import kubetester, read_secret, try_load
from kubetester.automation_config_tester import SCRAM_AGENT_USER, AutomationConfigTester
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
from pymongo import MongoClient
from pymongo.errors import OperationFailure
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


def _create_automation_agent_user(resource: MongoDB, ca_path: str):
    """
    CLOUDP-383102 workaround: create the automation agent user.

    When transitioning from disabled-auth → SCRAM-only, the keyfile on disk from the
    previous auth phase blocks the localhost exception for external connections. This
    prevents the automation agent from bootstrapping its SCRAM user, causing a deadlock.

    Uses pymongo to check if user exists (works from anywhere), uses kubectl exec
    to create the user (requires localhost exception inside the container).

    Tries each config server pod. When a pod is a secondary ("not primary"/
    NotWritablePrimary), advances to the next pod. Stops immediately on success or
    already-exists. Raises when every candidate fails so the caller does not silently
    burn another 600s waiting for a phase that will never come.
    """
    namespace = resource.namespace
    resource_name = resource.name
    password = read_secret(namespace, f"{resource_name}-agent-auth-secret")["automation-agent-password"]

    config_server_host = f"{resource.config_srv_pod_name(0)}.{resource_name}-cs:27017"

    # First, check if the user already exists by authenticating via pymongo with
    # SCRAM-SHA-256 explicitly so the probe does not fall back to SCRAM-SHA-1.
    try:
        client: MongoClient = MongoClient(
            config_server_host,
            username=SCRAM_AGENT_USER,
            password=password,
            authSource="admin",
            authMechanism="SCRAM-SHA-256",
            tls=True,
            tlsCAFile=ca_path,
            tlsAllowInvalidHostnames=True,
            directConnection=True,
            serverSelectionTimeoutMS=5000,
        )
        try:
            client.admin.command("ping")
            logger.info(f"{SCRAM_AGENT_USER} user already exists")
            return
        finally:
            client.close()
    except OperationFailure as e:
        if e.code == 18:
            logger.info(f"{SCRAM_AGENT_USER} user does not exist, will create")
        else:
            logger.warning(f"Unexpected error checking user: {e}")
    except Exception as e:
        logger.info(f"Could not verify user exists ({e}), will try to create")

    # User doesn't exist - create via kubectl exec (needs localhost exception).
    create_user_js = f"""
db.createUser({{
  user: '{SCRAM_AGENT_USER}',
  pwd: '{password}',
  roles: ['backup','clusterAdmin','dbAdminAnyDatabase','readWriteAnyDatabase','restore','userAdminAnyDatabase'].map(r=>({{role:r,db:'admin'}})),
  mechanisms: ['SCRAM-SHA-256']
}})
"""

    config_pods = [resource.config_srv_pod_name(i) for i in range(resource["spec"]["configServerCount"])]
    errors = []
    for pod in config_pods:
        cmd = [
            "kubectl",
            "exec",
            "-n",
            namespace,
            pod,
            "-c",
            "mongodb-enterprise-database",
            "--",
            "env",
            "HOME=/tmp",
            "/usr/bin/mongosh",
            "--tls",
            "--tlsCAFile",
            "/mongodb-automation/tls/ca/ca-pem",
            "--tlsAllowInvalidHostnames",
            "--norc",
            "mongodb://localhost:27017/admin",
            "--eval",
            create_user_js,
        ]
        try:
            result = subprocess.run(cmd, capture_output=True, text=True, timeout=30)
        except subprocess.TimeoutExpired:
            errors.append(f"{pod}: timed out after 30s")
            logger.warning(f"Timed out creating automation agent user on {pod}")
            continue

        # Never include raw mongosh stdout/stderr: create_user_js contains the
        # password and mongosh may echo the eval'd script on error.
        lower = f"{result.stdout}\n{result.stderr}".lower()
        if result.returncode == 0 or "already exists" in lower:
            logger.info(f"Pre-created {SCRAM_AGENT_USER} user on {pod}")
            return
        if "not primary" in lower or "notwritableprimary" in lower:
            logger.info(f"{pod} is not primary, trying next config server pod")
            continue
        detail = f"returncode={result.returncode}"
        errors.append(f"{pod}: {detail}")
        logger.warning(f"Failed to create user on {pod}: {detail}")

    raise RuntimeError(
        f"CLOUDP-383102: Failed to create {SCRAM_AGENT_USER} user on any config server pod "
        f"({', '.join(config_pods)}). Errors: {'; '.join(errors)}"
    )


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
    def test_can_enable_scram_sha_256(self, sharded_cluster: MongoDB, ca_path: str):
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
                _create_automation_agent_user(sharded_cluster, ca_path)
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
      wait_until: mongo_resource_deleted_no_om
      timeout: 240
    """

    def test_noop(self):
        pass
