from dataclasses import dataclass
from typing import Dict, List, Optional

import kubernetes.client
from kubetester import read_configmap
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark
from tests import test_logger
from tests.conftest import (
    LEGACY_DEPLOYMENT_STATE_VERSION,
    LEGACY_MULTI_CLUSTER_OPERATOR_NAME,
    LEGACY_OPERATOR_NAME,
    MULTI_CLUSTER_OPERATOR_NAME,
    create_appdb_certs,
    get_central_cluster_name,
    get_custom_appdb_version,
    install_official_operator,
    log_deployments_info, LEGACY_OPERATOR_CHART,
)
from tests.multicluster.conftest import cluster_spec_list
from tests.upgrades import downscale_operator_deployment

CERT_PREFIX = "prefix"
logger = test_logger.get_test_logger(__name__)

appdb_version = get_custom_appdb_version()

"""
multicluster_appdb_state_operator_upgrade_downgrade ensures the correctness of the state configmaps of AppDB, when
upgrading/downgrading from/to the legacy state management (versions <= 1.27) and the current operator (from master)
while performing scaling operations accross multiple clusters.
It will always be pinned to version 1.27 (variable LEGACY_DEPLOYMENT_STATE_VERSION) for the initial deployment, so
in the future will test upgrade paths of multiple versions at a time (e.g 1.27 -> currently developed 1.30), even
though we don't officially support these paths.

The workflow of this test is the following
Install Operator 1.27 -> Deploy OM/AppDB -> Upgrade operator (dev version) -> Scale AppDB
-> Downgrade Operator to 1.27 -> Scale AppDB
At each step, we verify that the state is correct
"""


def assert_cm_expected_data(
    name: str, namespace: str, expected_data: Optional[Dict], central_cluster_client: kubernetes.client.ApiClient
):
    # We try to read the configmap, and catch the exception in case iy doesn't exist
    # We later assert this is expected from the test, when expected_data is None
    state_configmap_data = None
    try:
        state_configmap_data = read_configmap(namespace, name, central_cluster_client)
    except Exception as e:
        logger.error(f"Error when trying to read the configmap {name} in namespace {namespace}: {e}")

    logger.debug(f"Asserting correctness of configmap {name} in namespace {namespace}")

    if state_configmap_data is None:
        logger.debug(f"Couldn't find configmap {name} in namespace {namespace}")
        assert None == expected_data
    else:
        logger.debug(f"The configmap {name} in namespace {namespace} contains: {state_configmap_data}")
    logger.debug(f"The expected data is: {expected_data}")
    assert (
        state_configmap_data == expected_data
    ), f"ConfigMap data mismatch, actual: {state_configmap_data} != expected: {expected_data}"


# This data class helps to store the different test cases below.
# Each test case defines the AppDB replicas distribution over member clusters, and the expected values of all configmaps
# after reconciliation
@dataclass
class TestCase:
    cluster_spec: List[Dict[str, int]]
    expected_db_cluster_mapping: Dict[str, str]
    expected_db_member_spec: Dict[str, str]
    expected_db_state: Optional[Dict[str, str]]


# Initial Cluster Spec Test Case
initial_cluster_spec = TestCase(
    cluster_spec=cluster_spec_list(["kind-e2e-cluster-2"], [3]),
    expected_db_cluster_mapping={
        "kind-e2e-cluster-2": "0",
    },
    expected_db_member_spec={
        "kind-e2e-cluster-2": "3",
    },
    expected_db_state=None,
)

# Scale on Upgrade Test Case
scale_on_upgrade = TestCase(
    cluster_spec=cluster_spec_list(["kind-e2e-cluster-3", "kind-e2e-cluster-1", "kind-e2e-cluster-2"], [1, 1, 3]),
    # Cluster 2 was already in the mapping, it keeps its index. Cluster 3 has index one because it appears before
    # cluster 1 in the above spec list
    expected_db_cluster_mapping={
        "kind-e2e-cluster-1": "2",
        "kind-e2e-cluster-2": "0",
        "kind-e2e-cluster-3": "1",
    },
    # After full deployment, the LastAppliedMemberSpec Config Map should match the above cluster spec list
    expected_db_member_spec={
        "kind-e2e-cluster-1": "1",
        "kind-e2e-cluster-2": "3",
        "kind-e2e-cluster-3": "1",
    },
    # The "state" should contain the same fields as above, but marshalled in a single map
    expected_db_state={
        "state": f'{{"clusterMapping":{{"kind-e2e-cluster-1":2,"kind-e2e-cluster-2":0,"kind-e2e-cluster-3":1}},"lastAppliedMemberSpec":{{"kind-e2e-cluster-1":1,"kind-e2e-cluster-2":3,"kind-e2e-cluster-3":1}},"lastAppliedMongoDBVersion":"{appdb_version}"}}'
    },
)

# Scale on Downgrade Test Case
scale_on_downgrade = TestCase(
    cluster_spec=cluster_spec_list(["kind-e2e-cluster-3", "kind-e2e-cluster-1", "kind-e2e-cluster-2"], [1, 2, 0]),
    # No new cluster introduced, the mapping stays the same
    expected_db_cluster_mapping={
        "kind-e2e-cluster-1": "2",
        "kind-e2e-cluster-2": "0",
        "kind-e2e-cluster-3": "1",
    },
    # Member spec should match the new cluster spec list
    expected_db_member_spec={
        "kind-e2e-cluster-1": "2",
        "kind-e2e-cluster-2": "0",
        "kind-e2e-cluster-3": "1",
    },
    # State isn't updated as we downgrade to an operator version that doesn't manage the new state format
    expected_db_state=scale_on_upgrade.expected_db_state,
)


@fixture(scope="module")
def ops_manager(
    namespace: str,
    custom_version: str,
    custom_appdb_version: str,
    multi_cluster_issuer_ca_configmap: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("multicluster_appdb_om.yaml"), namespace=namespace
    )
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource["spec"]["version"] = custom_version
    resource["spec"]["topology"] = "MultiCluster"
    # OM cluster specs (not rescaled during the test)
    resource["spec"]["clusterSpecList"] = cluster_spec_list(["kind-e2e-cluster-1"], [1])

    resource.allow_mdb_rc_versions()
    logger.info(f"Creating admin secret in cluster {get_central_cluster_name()}")
    resource.create_admin_secret(api_client=central_cluster_client)

    resource["spec"]["backup"] = {"enabled": False}
    resource["spec"]["applicationDatabase"] = {
        "topology": "MultiCluster",
        "clusterSpecList": initial_cluster_spec.cluster_spec,
        "version": custom_appdb_version,
        "agent": {"logLevel": "DEBUG"},
        "security": {
            "certsSecretPrefix": CERT_PREFIX,
            "tls": {"ca": multi_cluster_issuer_ca_configmap},
        },
    }

    return resource


@mark.e2e_multi_cluster_appdb_state_operator_upgrade_downgrade
class TestOpsManagerCreation:
    """
    Ensure correct deployment and state of AppDB, with operator version 1.27 installed.

    """

    # If we want to add CRDs, clone repo at a specific tag and apply CRDs
    def test_install_legacy_state_official_operator(
        self,
        namespace: str,
        managed_security_context,
        operator_installation_config,
        central_cluster_name,
        central_cluster_client,
        member_cluster_clients,
        member_cluster_names,
    ):
        logger.info(
            f"Installing the operator from chart {LEGACY_OPERATOR_CHART}, with version {LEGACY_DEPLOYMENT_STATE_VERSION}"
        )
        operator = install_official_operator(
            namespace,
            managed_security_context,
            operator_installation_config,
            central_cluster_name,
            central_cluster_client,
            member_cluster_clients,
            member_cluster_names,
            custom_operator_version=LEGACY_DEPLOYMENT_STATE_VERSION,
            helm_chart_path=LEGACY_OPERATOR_CHART, # We are testing the upgrade from legacy state management, introduced in MEKO
            operator_name=LEGACY_OPERATOR_NAME,
        )
        operator.assert_is_running()
        # Dumping deployments in logs ensure we are using the correct operator version
        log_deployments_info(namespace)

    def test_create_appdb_certs_secret(
        self,
        namespace: str,
        multi_cluster_issuer: str,
        ops_manager: MongoDBOpsManager,
    ):
        create_appdb_certs(
            namespace,
            multi_cluster_issuer,
            ops_manager.name + "-db",
            cluster_index_with_members=[(0, 5), (1, 5), (2, 5)],
            cert_prefix=CERT_PREFIX,
        )

    def test_create_om(
        self,
        ops_manager: MongoDBOpsManager,
    ):
        ops_manager.update()
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=700)
        ops_manager.om_status().assert_reaches_phase(Phase.Running)

    def test_state_correctness(
        self, namespace: str, ops_manager: MongoDBOpsManager, central_cluster_client: kubernetes.client.ApiClient
    ):
        configmap_name = f"{ops_manager.name}-db-cluster-mapping"
        # After deploying the old operator, we expect legacy state in the cluster
        expected_data = initial_cluster_spec.expected_db_cluster_mapping
        assert_cm_expected_data(configmap_name, namespace, expected_data, central_cluster_client)

        configmap_name = f"{ops_manager.name}-db-member-spec"
        expected_data = initial_cluster_spec.expected_db_member_spec
        # The expected data is the same for the initial deployment
        assert_cm_expected_data(configmap_name, namespace, expected_data, central_cluster_client)


@mark.e2e_multi_cluster_appdb_state_operator_upgrade_downgrade
class TestOperatorUpgrade:
    """
    Upgrade the operator to latest dev version, scale AppDB, and ensure state correctness.
    """

    def test_downscale_latest_official_operator(self, namespace: str):
        # Scale down the existing operator deployment to 0. This is needed as we are initially installing MEKO
        # and replacing it with MCK
        downscale_operator_deployment(deployment_name=LEGACY_MULTI_CLUSTER_OPERATOR_NAME, namespace=namespace)

    def test_install_default_operator(self, namespace: str, multi_cluster_operator: Operator):
        logger.info("Installing the operator built from master")
        multi_cluster_operator.assert_is_running()
        # Dumping deployments in logs ensure we are using the correct operator version
        log_deployments_info(namespace)

    def test_scale_appdb(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        # Reordering the clusters triggers a change in the state
        ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = scale_on_upgrade.cluster_spec
        ops_manager.update()
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=500)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=250)

    def test_migrated_state_correctness(
        self, namespace: str, ops_manager: MongoDBOpsManager, central_cluster_client: kubernetes.client.ApiClient
    ):
        configmap_name = f"{ops_manager.name}-db-state"
        # After upgrading the operator, we expect the state to be migrated to the new configmap
        assert_cm_expected_data(configmap_name, namespace, scale_on_upgrade.expected_db_state, central_cluster_client)

    def test_old_state_still_exists(
        self, namespace: str, ops_manager: MongoDBOpsManager, central_cluster_client: kubernetes.client.ApiClient
    ):
        configmap_name = f"{ops_manager.name}-db-cluster-mapping"
        assert_cm_expected_data(
            configmap_name, namespace, scale_on_upgrade.expected_db_cluster_mapping, central_cluster_client
        )
        configmap_name = f"{ops_manager.name}-db-member-spec"
        assert_cm_expected_data(
            configmap_name, namespace, scale_on_upgrade.expected_db_member_spec, central_cluster_client
        )


@mark.e2e_multi_cluster_appdb_state_operator_upgrade_downgrade
class TestOperatorDowngrade:
    """
    Downgrade the Operator to 1.27, scale AppDB and ensure state correctness.
    """

    def test_downscale_default_operator(self, namespace: str):
        downscale_operator_deployment(deployment_name=MULTI_CLUSTER_OPERATOR_NAME, namespace=namespace)

    def test_install_legacy_state_official_operator(
        self,
        namespace: str,
        managed_security_context,
        operator_installation_config,
        central_cluster_name,
        central_cluster_client,
        member_cluster_clients,
        member_cluster_names,
    ):
        logger.info(f"Downgrading the operator to version {LEGACY_DEPLOYMENT_STATE_VERSION}, from chart {LEGACY_OPERATOR_CHART}")
        operator = install_official_operator(
            namespace,
            managed_security_context,
            operator_installation_config,
            central_cluster_name,
            central_cluster_client,
            member_cluster_clients,
            member_cluster_names,
            custom_operator_version=LEGACY_DEPLOYMENT_STATE_VERSION,
            helm_chart_path=LEGACY_OPERATOR_CHART,
            operator_name=LEGACY_OPERATOR_NAME,
        )
        operator.assert_is_running()
        # Dumping deployments in logs ensure we are using the correct operator version
        log_deployments_info(namespace)

    def test_om_running_after_downgrade(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager.appdb_status().assert_reaches_phase(Phase.Pending, timeout=60)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=350)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=200)

    def test_scale_appdb(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = scale_on_downgrade.cluster_spec
        ops_manager.update()
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=200)

    def test_state_correctness_after_downgrade(
        self, namespace: str, ops_manager: MongoDBOpsManager, central_cluster_client: kubernetes.client.ApiClient
    ):
        configmap_name = f"{ops_manager.name}-db-cluster-mapping"
        assert_cm_expected_data(
            configmap_name, namespace, scale_on_downgrade.expected_db_cluster_mapping, central_cluster_client
        )
        configmap_name = f"{ops_manager.name}-db-member-spec"
        assert_cm_expected_data(
            configmap_name, namespace, scale_on_downgrade.expected_db_cluster_mapping, central_cluster_client
        )
        configmap_name = f"{ops_manager.name}-db-state"
        assert_cm_expected_data(configmap_name, namespace, scale_on_downgrade.expected_db_state, central_cluster_client)
