from typing import Dict

import pytest
from kubetester import read_configmap, try_load
from kubetester.certs import create_mongodb_tls_certs, create_sharded_cluster_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongotester import ReplicaSetTester, ShardedClusterTester
from kubetester.phase import Phase
from tests import test_logger
from tests.conftest import (
    get_central_cluster_client,
    get_default_operator,
    install_legacy_deployment_state_meko,
    log_deployments_info,
)
from tests.constants import LEGACY_OPERATOR_NAME, OPERATOR_NAME
from tests.upgrades import downscale_operator_deployment

MDB_RESOURCE = "sh001-base"
MDB_RS_RESOURCE = "rs"
CERT_PREFIX = "prefix"

logger = test_logger.get_test_logger(__name__)

"""
e2e_sharded_cluster_operator_upgrade_v1_27_to_mck ensures the correct operation of a single cluster sharded cluster, when
upgrading/downgrading from/to the legacy state management (versions <= 1.27) and the current operator (from master)
while performing scaling operations.
It will always be pinned to version 1.27 (variable LEGACY_DEPLOYMENT_STATE_VERSION) for the initial deployment, so
in the future will test upgrade paths of multiple versions at a time (e.g 1.27 -> currently developed 1.30), even
though we don't officially support these paths.

The workflow of this test is the following
Install Operator 1.27 -> Deploy Sharded Cluster -> Scale Up Cluster -> Upgrade operator (dev version) -> Scale down
-> Downgrade Operator to 1.27 -> Scale up
If the sharded cluster resource correctly reconciles after upgrade/downgrade and scaling steps, we assume it works
correctly.
"""


# TODO CLOUDP-318100: this test should eventually be updated and not pinned to 1.27 anymore


def log_state_configmap(namespace: str):
    configmap_name = f"{MDB_RESOURCE}-state"
    try:
        state_configmap_data = read_configmap(namespace, configmap_name)
    except Exception as e:
        logger.error(f"Error when trying to read the configmap {configmap_name} in namespace {namespace}: {e}")
        return
    logger.debug(f"state_configmap_data: {state_configmap_data}")


# Fixtures
@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str) -> str:
    return create_sharded_cluster_certs(
        namespace,
        MDB_RESOURCE,
        shards=1,
        mongod_per_shard=3,
        config_servers=3,
        mongos=2,
        secret_prefix=f"{CERT_PREFIX}-",
    )


@pytest.fixture(scope="function")
def sharded_cluster(
    issuer_ca_configmap: str,
    namespace: str,
    server_certs: str,
    custom_mdb_version: str,
):
    resource = MongoDB.from_yaml(
        yaml_fixture("sharded-cluster.yaml"),
        namespace=namespace,
        name=MDB_RESOURCE,
    ).configure(om=None)

    if try_load(resource):
        return resource

    resource.set_version(custom_mdb_version)
    resource["spec"]["mongodsPerShardCount"] = 2
    resource["spec"]["configServerCount"] = 2
    resource["spec"]["mongosCount"] = 1
    resource["spec"]["persistent"] = True
    resource.configure_custom_tls(issuer_ca_configmap, CERT_PREFIX)

    return resource


@pytest.fixture(scope="module")
def replica_set_certs(issuer: str, namespace: str):
    return create_mongodb_tls_certs(issuer, namespace, MDB_RS_RESOURCE, f"prefix-{MDB_RS_RESOURCE}-cert")


@pytest.fixture(scope="module")
def replica_set(
    issuer_ca_configmap: str,
    namespace: str,
    replica_set_certs: str,
    custom_mdb_version: str,
):
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-basic.yaml"),
        namespace=namespace,
        name=MDB_RS_RESOURCE,
    ).configure(om=None)

    if try_load(resource):
        return resource

    resource.set_version(custom_mdb_version)
    resource.configure_custom_tls(issuer_ca_configmap, CERT_PREFIX)

    return resource


@pytest.mark.e2e_sharded_cluster_operator_upgrade_v1_27_to_mck
class TestShardedClusterDeployment:
    def test_install_legacy_deployment_state_meko(
        self,
        namespace: str,
        managed_security_context: str,
        operator_installation_config: Dict[str, str],
    ):
        install_legacy_deployment_state_meko(namespace, managed_security_context, operator_installation_config)

    def test_create_sharded_cluster(self, sharded_cluster: MongoDB):
        sharded_cluster.update()
        sharded_cluster.assert_reaches_phase(phase=Phase.Running, timeout=350)

    def test_scale_up_sharded_cluster(self, sharded_cluster: MongoDB):
        sharded_cluster["spec"]["mongodsPerShardCount"] = 3
        sharded_cluster["spec"]["configServerCount"] = 3
        sharded_cluster.update()
        sharded_cluster.assert_reaches_phase(phase=Phase.Running, timeout=300)


@pytest.mark.e2e_sharded_cluster_operator_upgrade_v1_27_to_mck
class TestReplicaSetDeployment:
    def test_create_replica_set(self, replica_set: MongoDB):
        replica_set.update()
        replica_set.assert_reaches_phase(phase=Phase.Running, timeout=350)


@pytest.mark.e2e_sharded_cluster_operator_upgrade_v1_27_to_mck
class TestOperatorUpgrade:

    def test_downscale_latest_official_operator(self, namespace: str):
        # Scale down the existing operator deployment to 0. This is needed as we are initially installing MEKO
        # and replacing it with MCK
        downscale_operator_deployment(deployment_name=LEGACY_OPERATOR_NAME, namespace=namespace)

    def test_upgrade_operator(self, namespace: str, operator_installation_config: dict[str, str]):
        operator = get_default_operator(
            namespace, operator_installation_config=operator_installation_config, apply_crds_first=True
        )
        operator.assert_is_running()
        logger.info("Installing the operator built from master")
        # Dumping deployments in logs ensures we are using the correct operator version
        log_deployments_info(namespace)

    def test_sharded_cluster_reconciled(self, sharded_cluster: MongoDB, namespace: str):
        sharded_cluster.assert_abandons_phase(phase=Phase.Running, timeout=200)
        sharded_cluster.assert_reaches_phase(phase=Phase.Running, timeout=500)
        logger.debug("State configmap after upgrade")
        log_state_configmap(namespace)

    def test_assert_connectivity(self, ca_path: str):
        ShardedClusterTester(MDB_RESOURCE, 1, ssl=True, ca_path=ca_path).assert_connectivity()

    def test_replica_set_reconciled(self, replica_set: MongoDB):
        replica_set.assert_reaches_phase(phase=Phase.Running, timeout=850, ignore_errors=True)

    def test_assert_connectivity_replica_set(self, ca_path: str):
        ReplicaSetTester(MDB_RS_RESOURCE, 3, ssl=True, ca_path=ca_path).assert_connectivity()

    def test_scale_down_sharded_cluster(self, sharded_cluster: MongoDB, namespace: str):
        sharded_cluster.load()
        # Scale down both by 1
        sharded_cluster["spec"]["mongodsPerShardCount"] = 2
        sharded_cluster["spec"]["configServerCount"] = 2
        sharded_cluster.update()
        sharded_cluster.assert_reaches_phase(phase=Phase.Running, timeout=450)
        logger.debug("State configmap after upgrade and scaling")
        log_state_configmap(namespace)


@pytest.mark.e2e_sharded_cluster_operator_upgrade_v1_27_to_mck
class TestOperatorDowngrade:
    def test_downscale_default_operator(self, namespace: str):
        downscale_operator_deployment(deployment_name=OPERATOR_NAME, namespace=namespace)

    def test_downgrade_to_legacy_deployment_state_meko(
        self,
        namespace: str,
        managed_security_context: str,
        operator_installation_config: Dict[str, str],
    ):
        install_legacy_deployment_state_meko(namespace, managed_security_context, operator_installation_config)

    def test_sharded_cluster_reconciled(self, sharded_cluster: MongoDB):
        sharded_cluster.assert_abandons_phase(phase=Phase.Running, timeout=200)
        sharded_cluster.assert_reaches_phase(phase=Phase.Running, timeout=850, ignore_errors=True)

    def test_assert_connectivity(self, ca_path: str):
        ShardedClusterTester(MDB_RESOURCE, 1, ssl=True, ca_path=ca_path).assert_connectivity()

    def test_replica_set_reconciled(self, replica_set: MongoDB):
        replica_set.assert_reaches_phase(phase=Phase.Running, timeout=850, ignore_errors=True)

    def test_assert_connectivity_replica_set(self, ca_path: str):
        ReplicaSetTester(MDB_RS_RESOURCE, 3, ssl=True, ca_path=ca_path).assert_connectivity()

    def test_scale_up_sharded_cluster(self, sharded_cluster: MongoDB):
        sharded_cluster.load()
        sharded_cluster["spec"]["mongodsPerShardCount"] = 3
        sharded_cluster["spec"]["configServerCount"] = 3
        sharded_cluster.update()
        sharded_cluster.assert_reaches_phase(phase=Phase.Running, timeout=350)
