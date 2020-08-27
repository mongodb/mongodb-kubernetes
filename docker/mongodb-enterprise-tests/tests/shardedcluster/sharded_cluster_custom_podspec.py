from kubetester.custom_podspec import assert_stateful_set_podspec
from kubetester.kubetester import fixture as yaml_fixture, KubernetesTester
from kubetester.mongodb import MongoDB, Phase
from pytest import fixture, mark

SHARD_WEIGHT = 50
MONGOS_WEIGHT = 40
CONFIG_WEIGHT = 30

SHARD_GRACE_PERIOD = 30
MONGOS_GRACE_PERIOD = 20
CONFIG_GRACE_PERIOD = 50

SHARD_TOPOLOGY_KEY = "shard"
MONGOS_TOPOLOGY_KEY = "mongos"
CONFIG_TOPOLOGY_KEY = "config"


@fixture(scope="module")
def sharded_cluster(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("sharded-cluster-custom-podspec.yaml"), namespace=namespace
    )
    resource.set_version(custom_mdb_version)
    return resource.create()


@mark.e2e_sharded_cluster_custom_podspec
def test_replica_set_reaches_running_phase(sharded_cluster):
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=600)
    assert "warnings" not in sharded_cluster["status"]


@mark.e2e_sharded_cluster_custom_podspec
def test_stateful_sets_spec_updated(sharded_cluster, namespace):
    appsv1 = KubernetesTester.clients("appsv1")
    config_sts = appsv1.read_namespaced_stateful_set(
        f"{sharded_cluster.name}-config", namespace
    )
    mongos_sts = appsv1.read_namespaced_stateful_set(
        f"{sharded_cluster.name}-mongos", namespace
    )
    shard_sts = appsv1.read_namespaced_stateful_set(
        f"{sharded_cluster.name}-0", namespace
    )

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

    containers = shard_sts.spec.template.spec.containers

    assert len(containers) == 2
    assert containers[0].name == "mongodb-enterprise-database"
    assert containers[1].name == "sharded-cluster-sidecar"

    resources = containers[1].resources

    assert resources.limits["cpu"] == "1"
    assert resources.requests["cpu"] == "500m"
