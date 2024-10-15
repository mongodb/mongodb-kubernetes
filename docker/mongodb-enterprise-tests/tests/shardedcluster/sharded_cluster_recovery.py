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


@mark.e2e_sharded_cluster_recovery
def test_install_operator(default_operator: Operator):
    default_operator.assert_is_running()


@mark.e2e_sharded_cluster_recovery
class TestShardedClusterRecoversBadOmConfiguration(KubernetesTester):
    """
    name: Sharded cluster broken OM connection
    description: |
      Creates a sharded cluster with a bad OM connection (public key is broken) and ensures it enters a failed state |
      Then the secret is fixed and the standalone is expected to reach good state eventually
    """

    def test_sharded_cluster_reaches_failed_state(self, mdb: MongoDB):
        secret = V1Secret(string_data={"publicApiKey": "wrongKey"})
        self.clients("corev1").patch_namespaced_secret("my-credentials", self.get_namespace(), secret)

        mdb.assert_reaches_phase(Phase.Failed, timeout=20)

        mdb.load()
        assert "You are not authorized for this resource" in mdb["status"]["message"]

    def test_recovery(self, mdb: MongoDB):
        secret = V1Secret(string_data={"publicApiKey": self.get_om_api_key()})
        self.clients("corev1").patch_namespaced_secret("my-credentials", self.get_namespace(), secret)

        # We need to ignore errors here because the CM change can be faster than the check
        mdb.assert_reaches_phase(Phase.Running, ignore_errors=True)
