from kubernetes.client import V1Secret
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.operator import Operator
from pytest import fixture, mark


@fixture(scope="module")
def mdb(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("sharded-cluster-single.yaml"), namespace=namespace)
    resource.set_version(custom_mdb_version)
    return resource.update()


@mark.e2e_sharded_cluster_secret
def test_install_operator(default_operator: Operator):
    default_operator.assert_is_running()


@mark.e2e_sharded_cluster_secret
class TestShardedClusterListensSecret(KubernetesTester):
    """
    name: ShardedCluster tracks configmap changes
    description: |
      Creates a sharded cluster, then changes secret - breaks the api key and checks that the reconciliation for the |
      standalone happened and it got into Failed state. Note, that this test cannot be run with 'make e2e .. light=true' |
      flag locally as secret must be recreated
    """

    def test_create_sharded_cluster(self, mdb: MongoDB):
        mdb.assert_reaches_phase(Phase.Running)

    def test_patch_config_map(self, mdb: MongoDB):
        secret = V1Secret(string_data={"publicApiKey": "wrongKey"})
        self.clients("corev1").patch_namespaced_secret("my-credentials", self.get_namespace(), secret)

        print('Patched the Secret - changed publicApiKey to "wrongKey"')
        mdb.assert_reaches_phase(Phase.Failed, timeout=20)
