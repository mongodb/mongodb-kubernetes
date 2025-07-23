from kubetester import try_load
from kubetester.custom_podspec import assert_stateful_set_podspec
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import is_default_architecture_static
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.shardedcluster.conftest import (
    enable_multi_cluster_deployment,
    get_member_cluster_clients_using_cluster_mapping,
)

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


@fixture(scope="function")
def sc(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("sharded-cluster-custom-podspec.yaml"), namespace=namespace)

    if try_load(resource):
        return resource

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()

    resource["spec"]["persistent"] = True

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    return resource.update()


@mark.e2e_sharded_cluster_custom_podspec
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_sharded_cluster_custom_podspec
def test_create_sharded_cluster(sc: MongoDB):
    sc.assert_reaches_phase(Phase.Running, timeout=1000)


@mark.e2e_sharded_cluster_custom_podspec
def test_stateful_sets_spec_updated(sc: MongoDB):
    for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
        cluster_idx = cluster_member_client.cluster_index

        shard0_sts_name = sc.shard_statefulset_name(0, cluster_idx)
        shard0_sts = cluster_member_client.read_namespaced_stateful_set(shard0_sts_name, sc.namespace)

        shard1_sts_name = sc.shard_statefulset_name(1, cluster_idx)
        shard1_sts = cluster_member_client.read_namespaced_stateful_set(shard1_sts_name, sc.namespace)

        mongos_sts_name = sc.mongos_statefulset_name(cluster_idx)
        mongos_sts = cluster_member_client.read_namespaced_stateful_set(mongos_sts_name, sc.namespace)

        config_sts_name = sc.config_srv_statefulset_name(cluster_idx)
        config_sts = cluster_member_client.read_namespaced_stateful_set(config_sts_name, sc.namespace)

        assert_stateful_set_podspec(
            shard0_sts.spec.template.spec,
            weight=SHARD0_WEIGHT,
            grace_period_seconds=SHARD0_GRACE_PERIOD,
            topology_key=SHARD0_TOPLOGY_KEY,
        )
        assert_stateful_set_podspec(
            shard1_sts.spec.template.spec,
            weight=SHARD_WEIGHT,
            grace_period_seconds=SHARD_GRACE_PERIOD,
            topology_key=SHARD_TOPOLOGY_KEY,
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

        if is_default_architecture_static():
            containers = shard0_sts.spec.template.spec.containers
            container_names = [container.name for container in containers]

            assert len(containers) == 4
            assert "mongodb-agent" in container_names
            assert "mongodb-enterprise-database" in container_names
            assert "mongodb-agent-operator-utilities" in container_names
            assert "sharded-cluster-sidecar-override" in container_names

            containers = shard1_sts.spec.template.spec.containers
            container_names = [container.name for container in containers]

            assert len(containers) == 4
            assert "mongodb-agent" in container_names
            assert "mongodb-enterprise-database" in container_names
            assert "mongodb-agent-operator-utilities" in container_names
            assert "sharded-cluster-sidecar" in container_names

            resources = containers[2].resources
        else:
            containers = shard1_sts.spec.template.spec.containers
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
