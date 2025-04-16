from kubetester import try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.operator import Operator
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.shardedcluster.conftest import enable_multi_cluster_deployment


@fixture(scope="function")
def sc(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("sharded-cluster-single.yaml"),
        namespace=namespace,
    )

    if try_load(resource):
        return resource

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    return resource.update()


@mark.e2e_sharded_cluster_secret
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_sharded_cluster_secret
class TestShardedClusterListensSecret:
    """
    name: ShardedCluster tracks configmap changes
    description: |
      Creates a sharded cluster, then changes secret - breaks the api key and checks that the reconciliation for the |
      standalone happened and it got into Failed state. Note, that this test cannot be run with 'make e2e .. light=true' |
      flag locally as secret must be recreated
    """

    def test_create_sharded_cluster(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running, timeout=1000)

    def test_patch_config_map(self, sc: MongoDB):
        KubernetesTester.update_secret(sc.namespace, "my-credentials", {"publicApiKey": "wrongKey"})

        print('Patched the Secret - changed publicApiKey to "wrongKey"')
        sc.assert_reaches_phase(Phase.Failed, timeout=100)
