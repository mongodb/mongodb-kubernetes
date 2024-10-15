from kubetester import try_load
from kubetester.custom_podspec import assert_stateful_set_podspec
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import is_static_containers_architecture
from kubetester.mongodb import MongoDB, Phase
from kubetester.operator import Operator
from pytest import fixture, mark

SHARD_WEIGHT = 50
MONGOS_WEIGHT = 40
CONFIG_WEIGHT = 30
SHARD0_WEIGHT = 100

SHARD_GRACE_PERIOD = 30
MONGOS_GRACE_PERIOD = 20
CONFIG_GRACE_PERIOD = 50
SHARD0_GRACE_PERIOD = 60

SHARD_TOPOLOGY_KEY = "shard"
MONGOS_TOPOLOGY_KEY = "mongos"
CONFIG_TOPOLOGY_KEY = "config"
SHARD0_TOPLOGY_KEY = "shardoverride"


@fixture(scope="module")
def sharded_cluster(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("sharded-cluster-custom-podspec.yaml"), namespace=namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["persistent"] = True
    try_load(resource)

    return resource


@mark.e2e_sharded_cluster_custom_podspec
def test_install_operator(default_operator: Operator):
    default_operator.assert_is_running()


@mark.e2e_sharded_cluster_custom_podspec
def test_replica_set_reaches_running_phase(sharded_cluster):
    sharded_cluster.update()
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_sharded_cluster_custom_podspec
def test_stateful_sets_spec_updated(sharded_cluster, namespace):
    appsv1 = KubernetesTester.clients("appsv1")
    config_sts = appsv1.read_namespaced_stateful_set(f"{sharded_cluster.name}-config", namespace)
    mongos_sts = appsv1.read_namespaced_stateful_set(f"{sharded_cluster.name}-mongos", namespace)
    shard0_sts = appsv1.read_namespaced_stateful_set(f"{sharded_cluster.name}-0", namespace)
    shard_sts = appsv1.read_namespaced_stateful_set(f"{sharded_cluster.name}-1", namespace)

    assert_stateful_set_podspec(
        config_sts.spec.template.spec,
        weight=CONFIG_WEIGHT,
        grace_period_seconds=CONFIG_GRACE_PERIOD,
        topology_key=CONFIG_TOPOLOGY_KEY,
    )
    assert_stateful_set_podspec(
        mongos_sts.spec.template.spec,
        weight=MONGOS_WEIGHT,
        grace_period_seconds=MONGOS_GRACE_PERIOD,
        topology_key=MONGOS_TOPOLOGY_KEY,
    )
    assert_stateful_set_podspec(
        shard_sts.spec.template.spec,
        weight=SHARD_WEIGHT,
        grace_period_seconds=SHARD_GRACE_PERIOD,
        topology_key=SHARD_TOPOLOGY_KEY,
    )

    assert_stateful_set_podspec(
        shard0_sts.spec.template.spec,
        weight=SHARD0_WEIGHT,
        grace_period_seconds=SHARD0_GRACE_PERIOD,
        topology_key=SHARD0_TOPLOGY_KEY,
    )
    containers = shard_sts.spec.template.spec.containers

    if is_static_containers_architecture():
        assert len(containers) == 3
        assert containers[0].name == "mongodb-agent"
        assert containers[1].name == "mongodb-enterprise-database"
        assert containers[2].name == "sharded-cluster-sidecar"

        containers = shard0_sts.spec.template.spec.containers
        assert len(containers) == 3
        assert containers[0].name == "mongodb-agent"
        assert containers[1].name == "mongodb-enterprise-database"
        assert containers[2].name == "sharded-cluster-sidecar-override"
        resources = containers[2].resources
    else:
        assert len(containers) == 2
        assert containers[0].name == "mongodb-enterprise-database"
        assert containers[1].name == "sharded-cluster-sidecar"

        containers = shard0_sts.spec.template.spec.containers
        assert len(containers) == 2
        assert containers[0].name == "mongodb-enterprise-database"
        assert containers[1].name == "sharded-cluster-sidecar-override"
        resources = containers[1].resources

    assert resources.limits["cpu"] == "1"
    assert resources.requests["cpu"] == "500m"
