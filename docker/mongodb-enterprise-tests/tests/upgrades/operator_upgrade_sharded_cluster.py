import pytest
from kubetester import create_or_update
from kubetester.certs import create_sharded_cluster_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongotester import ShardedClusterTester
from kubetester.operator import Operator

MDB_RESOURCE = "sh001-base"
CERT_PREFIX = "prefix"


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str) -> str:
    return create_sharded_cluster_certs(
        namespace,
        MDB_RESOURCE,
        shards=1,
        mongos_per_shard=3,
        config_servers=3,
        mongos=2,
        secret_prefix=f"{CERT_PREFIX}-",
    )


@pytest.fixture(scope="module")
def sharded_cluster(issuer_ca_configmap: str, namespace: str, server_certs: str, custom_mdb_version: str):
    resource = MongoDB.from_yaml(yaml_fixture("sharded-cluster.yaml"), namespace=namespace, name=MDB_RESOURCE)
    resource.set_version(custom_mdb_version)
    resource["spec"]["mongodsPerShardCount"] = 2
    resource["spec"]["configServerCount"] = 2
    resource["spec"]["mongosCount"] = 1
    resource["spec"]["persistent"] = True
    resource.configure_custom_tls(issuer_ca_configmap, CERT_PREFIX)

    return create_or_update(resource)


@pytest.mark.e2e_operator_upgrade_sharded_cluster
def test_install_latest_official_operator(official_operator: Operator):
    official_operator.assert_is_running()


@pytest.mark.e2e_operator_upgrade_sharded_cluster
def test_install_sharded_cluster(sharded_cluster: MongoDB):
    sharded_cluster.assert_reaches_phase(phase=Phase.Running)


@pytest.mark.e2e_operator_upgrade_sharded_cluster
def test_scale_up_sharded_cluster(sharded_cluster: MongoDB):
    sharded_cluster.load()
    sharded_cluster["spec"]["mongodsPerShardCount"] = 3
    sharded_cluster["spec"]["configServerCount"] = 3
    create_or_update(sharded_cluster)

    sharded_cluster.assert_reaches_phase(phase=Phase.Running, timeout=800)


@pytest.mark.e2e_operator_upgrade_sharded_cluster
def test_upgrade_operator(default_operator: Operator):
    default_operator.assert_is_running()


@pytest.mark.e2e_operator_upgrade_sharded_cluster
def test_sharded_cluster_reconciled(sharded_cluster: MongoDB):
    sharded_cluster.assert_abandons_phase(phase=Phase.Running, timeout=300)
    sharded_cluster.assert_reaches_phase(phase=Phase.Running, timeout=800)


@pytest.mark.e2e_operator_upgrade_sharded_cluster
def test_assert_connectivity(ca_path: str):
    ShardedClusterTester(MDB_RESOURCE, 1, ssl=True, ca_path=ca_path).assert_connectivity()


@pytest.mark.e2e_operator_upgrade_sharded_cluster
def test_scale_down_sharded_cluster(sharded_cluster: MongoDB):
    sharded_cluster.load()
    sharded_cluster["spec"]["mongodsPerShardCount"] = 2
    sharded_cluster["spec"]["configServerCount"] = 2
    create_or_update(sharded_cluster)

    sharded_cluster.assert_reaches_phase(phase=Phase.Running, timeout=800)
