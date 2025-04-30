from kubetester import try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import is_multi_cluster
from kubetester.mongodb import MongoDB, Phase
from kubetester.operator import Operator
from pytest import fixture, mark
from tests.shardedcluster.conftest import enable_multi_cluster_deployment


@fixture(scope="function")
def sc(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("sharded-cluster-single.yaml"), namespace=namespace)

    if try_load(resource):
        return resource

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()

    if is_multi_cluster():
        enable_multi_cluster_deployment(
            resource=resource,
            shard_members_array=[1, 1, 1],
            mongos_members_array=[1, None, None],
            configsrv_members_array=[None, 1, None],
        )

    return resource.update()


@mark.e2e_sharded_cluster_recovery
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_sharded_cluster_recovery
class TestShardedClusterRecoversBadOmConfiguration:
    """
    name: Sharded cluster broken OM connection
    description: |
      Creates a sharded cluster with a bad OM connection (public key is broken) and ensures it enters a failed state |
      Then the secret is fixed and the standalone is expected to reach good state eventually
    """

    def test_create_sharded_cluster(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running, timeout=1000)

    def test_sharded_cluster_reaches_failed_state(self, sc: MongoDB):
        secret_data = {"publicApiKey": "wrongKey"}
        KubernetesTester.update_secret(sc.namespace, "my-credentials", secret_data)

        sc.assert_reaches_phase(Phase.Failed, timeout=20)

        sc.load()
        assert "You are not authorized for this resource" in sc["status"]["message"]

    def test_recovery(self, sc: MongoDB):
        secret_data = {"publicApiKey": KubernetesTester.get_om_api_key()}
        KubernetesTester.update_secret(sc.namespace, "my-credentials", secret_data)

        # We need to ignore errors here because the CM change can be faster than the check
        sc.assert_reaches_phase(Phase.Running, ignore_errors=True)
