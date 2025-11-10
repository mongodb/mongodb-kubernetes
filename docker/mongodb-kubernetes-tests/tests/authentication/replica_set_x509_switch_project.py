import pytest
from kubetester import (
    create_or_update_configmap,
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
from kubetester.mongotester import ReplicaSetTester
from kubetester.phase import Phase

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


@pytest.mark.e2e_replica_set_x509_switch_project
class TestReplicaSetCreationAndProjectSwitch(KubernetesTester):
    """
    E2E test suite for replica set creation, user connectivity with X509 authentication and switching Ops Manager project reference.
    """

    def test_create_replica_set(self, replica_set: MongoDB):
        """
        Test replica set creation ensuring resources are applied correctly and set reaches Running phase.
        """
        replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    def test_ops_manager_state_correctly_updated_in_initial_replica_set(self, replica_set: MongoDB):
        """
        Ensure Ops Manager state is correctly updated in the original replica set.
        """
        tester = replica_set.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled("MONGODB-X509")
        tester.assert_authoritative_set(True)
        tester.assert_authentication_enabled()
        tester.assert_expected_users(0)

    def test_switch_replica_set_project(self, replica_set: MongoDB, namespace: str):
        """
        Modify the replica set to switch its Ops Manager reference to a new project and verify lifecycle.
        """
        original_configmap = read_configmap(namespace=namespace, name="my-project")
        new_project_name = namespace + "-" + "second"
        new_project_configmap = create_or_update_configmap(
            namespace=namespace,
            name=new_project_name,
            data={
                "baseUrl": original_configmap["baseUrl"],
                "projectName": new_project_name,
                "orgId": original_configmap["orgId"],
            },
        )

        replica_set["spec"]["opsManager"]["configMapRef"]["name"] = new_project_configmap
        replica_set.update()

        replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    def test_ops_manager_state_correctly_updated_in_moved_replica_set(self, replica_set: MongoDB):
        """
        Ensure Ops Manager state is correctly updated in the moved replica set after the project switch.
        """
        tester = replica_set.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled("MONGODB-X509")
        tester.assert_authoritative_set(True)
        tester.assert_authentication_enabled()
        tester.assert_expected_users(0)
