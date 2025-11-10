import pytest
from kubetester import (
    read_configmap,
    try_load,
)
from kubetester.certs import (
    ISSUER_CA_NAME,
    create_agent_tls_certs,
    create_mongodb_tls_certs,
)
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB

from .replica_set_switch_project_helper import (
    ReplicaSetCreationAndProjectSwitchTestHelper,
)

# Constants
MDB_RESOURCE_NAME = "replica-set-x509-switch-project"


@pytest.fixture(scope="module")
def replica_set(namespace: str, server_certs: str, agent_certs: str, issuer_ca_configmap: str) -> MongoDB:
    """
    Fixture to initialize the MongoDB resource for the replica set.

    Dynamically updates the resource configuration based on the test context.
    """
    resource = MongoDB.from_yaml(
        load_fixture("replica-set-x509-to-scram-256.yaml"), name=MDB_RESOURCE_NAME, namespace=namespace
    )

    if try_load(resource):
        return resource

    resource["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    return resource.update()


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    return create_mongodb_tls_certs(ISSUER_CA_NAME, namespace, MDB_RESOURCE_NAME, f"{MDB_RESOURCE_NAME}-cert")


@pytest.fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_agent_tls_certs(issuer, namespace, MDB_RESOURCE_NAME)


@pytest.fixture(scope="module")
def test_helper(replica_set: MongoDB, namespace: str) -> ReplicaSetCreationAndProjectSwitchTestHelper:
    return ReplicaSetCreationAndProjectSwitchTestHelper(
        replica_set=replica_set, namespace=namespace, authentication_mechanism="MONGODB-X509"
    )


@pytest.mark.e2e_replica_set_x509_switch_project
class TestReplicaSetCreationAndProjectSwitch(KubernetesTester):
    """
    E2E test suite for replica set creation, user connectivity with X509 authentication and switching Ops Manager project reference.
    """

    def test_create_replica_set(self, test_helper: ReplicaSetCreationAndProjectSwitchTestHelper):
        test_helper.test_create_replica_set()

    def test_ops_manager_state_correctly_updated_in_initial_replica_set(
        self, test_helper: ReplicaSetCreationAndProjectSwitchTestHelper
    ):
        test_helper.test_ops_manager_state_with_expected_authentication(expected_users=0)

    def test_switch_replica_set_project(
        self, test_helper: ReplicaSetCreationAndProjectSwitchTestHelper, namespace: str
    ):
        original_configmap = read_configmap(namespace=namespace, name="my-project")
        test_helper.test_switch_replica_set_project(
            original_configmap, new_project_configmap_name=namespace + "-" + "second"
        )

    def test_ops_manager_state_correctly_updated_in_moved_replica_set(
        self, test_helper: ReplicaSetCreationAndProjectSwitchTestHelper
    ):
        test_helper.test_ops_manager_state_with_expected_authentication(expected_users=0)
