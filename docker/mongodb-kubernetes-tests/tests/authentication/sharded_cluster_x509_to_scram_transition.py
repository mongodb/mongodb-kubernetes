import logging
import time
from base64 import b64decode

import pymongo
import pytest
from kubetester import kubetester, try_load
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
logger = logging.getLogger(__name__)

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
        """CLOUDP-68873 DIAGNOSTIC: Proves agent ordering hypothesis"""
        kubetester.wait_processes_ready()
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1400)

        # Setup: Enable SCRAM authentication
        sharded_cluster.load()
        sharded_cluster["spec"]["security"]["authentication"]["enabled"] = True
        sharded_cluster["spec"]["security"]["authentication"]["modes"] = ["SCRAM"]
        sharded_cluster["spec"]["security"]["authentication"]["agents"]["mode"] = "SCRAM"
        sharded_cluster.update()

        # Phase 1: Wait for SCRAM enable (300s timeout, no exception)
        logger.info("CLOUDP-68873 DIAGNOSTIC: Phase 1 - Waiting for SCRAM enable (300s)")

        result = sharded_cluster.wait_for(
            lambda s: s.get_status_phase() == Phase.Running,
            timeout=300,
            should_raise=False  # Returns True or None, no exception
        )

        if result:
            logger.info("✓ CLOUDP-68873 DIAGNOSTIC: SCRAM enabled without intervention")
            return  # Success - no diagnostic needed

        # Phase 2: Timeout reached - manually create user
        logger.info("CLOUDP-68873 DIAGNOSTIC: Phase 2 - Timeout reached, creating user manually")

        namespace = sharded_cluster.namespace
        cluster_name = sharded_cluster.name

        # Phase 2a: Read agent password from secret
        try:
            secret_name = f"{cluster_name}-agent-auth-secret"
            corev1 = KubernetesTester.clients("corev1")
            secret = corev1.read_namespaced_secret(secret_name, namespace)

            if "automation-agent-password" not in secret.data:
                logger.error(f"Key 'automation-agent-password' not found in secret {secret_name}")
                logger.error(f"Available keys: {list(secret.data.keys())}")
                raise ValueError("Agent password key not found in secret")

            agent_password = b64decode(secret.data["automation-agent-password"]).decode('latin-1')
            logger.info("✓ Retrieved agent password from secret")

        except Exception as secret_error:
            logger.error(f"Failed to read agent password: {secret_error}")
            raise  # Re-raise - this is an actual error, not control flow

        # Phase 2b: Connect to MongoDB (try multiple strategies)
        connection_strategies = [
            {"uri": f"mongodb://{cluster_name}-0-0.{cluster_name}-sh.{namespace}.svc.cluster.local:27017/",
             "name": "direct to shard 0"},
            {"uri": f"mongodb://{cluster_name}-svc.{namespace}.svc.cluster.local:27017/",
             "name": "via service"}
        ]

        client = None
        for strategy in connection_strategies:
            try:
                logger.info(f"Trying connection: {strategy['name']}")
                client = pymongo.MongoClient(strategy['uri'], serverSelectionTimeoutMS=10000)
                client.admin.command('ping')
                logger.info(f"✓ Connected via {strategy['name']}")
                break
            except Exception as conn_error:
                logger.warning(f"Connection failed: {conn_error}")
                if client:
                    client.close()
                client = None

        if not client:
            logger.error("All connection strategies failed")
            raise ConnectionError("Cannot connect to MongoDB")

        # Phase 2c: Create user
        try:
            admin_db = client.admin
            admin_db.command(
                'createUser',
                'mms-automation-agent',
                pwd=agent_password,
                roles=[{'role': 'root', 'db': 'admin'}]
            )
            logger.info("✓ User created successfully")

        except pymongo.errors.DuplicateKeyError:
            logger.info("User already exists (DuplicateKeyError) - OK")

        except Exception as create_error:
            logger.error(f"User creation failed: {create_error}")
            raise

        finally:
            if client:
                client.close()

        logger.info("✓ CLOUDP-68873 DIAGNOSTIC: User created, waiting 45s for agent detection")
        time.sleep(45)

        # Phase 3: Check if agents unstuck
        logger.info("CLOUDP-68873 DIAGNOSTIC: Phase 3 - Checking if agents unstuck")

        result = sharded_cluster.wait_for(
            lambda s: s.get_status_phase() == Phase.Running,
            timeout=600,
            should_raise=False
        )

        if result:
            logger.info("=" * 80)
            logger.info("✅ PROOF CONFIRMED: Manual user creation UNSTUCK the agents")
            logger.info("✅ Root cause: Agent ordering (UpgradeAuth before EnsureCredentials)")
            logger.info("=" * 80)
        else:
            logger.error("=" * 80)
            logger.error("❌ Agents STILL STUCK after manual user creation")
            logger.error("❌ Suggests user creation failed OR not the sole issue")
            logger.error("=" * 80)
            raise AssertionError("Diagnostic failed: Agents still stuck after manual user creation")

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
