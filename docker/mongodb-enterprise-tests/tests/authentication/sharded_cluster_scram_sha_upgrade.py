import pytest
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongotester import ShardedClusterTester

MDB_RESOURCE = "my-sharded-cluster-scram-sha-1"


@pytest.mark.e2e_sharded_cluster_scram_sha_1_upgrade
class TestCreateScramSha1ShardedCluster(KubernetesTester):
    """
    description: |
      Creates a ShardedCluster with SCRAM-SHA-1 authentication
    """

    def test_create_sharded_cluster(self, custom_mdb_version: str):
        resource = MongoDB.from_yaml(load_fixture("sharded-cluster-scram-sha-1.yaml"), namespace=self.namespace)
        resource.set_version(custom_mdb_version)
        resource.update()

        resource.assert_reaches_phase(Phase.Running)

    def test_assert_connectivity(self):
        ShardedClusterTester(MDB_RESOURCE, 2).assert_connectivity()

    def test_ops_manager_state_updated_correctly(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
        tester.assert_authentication_enabled()

        tester.assert_expected_users(0)
        tester.assert_authoritative_set(True)


@pytest.mark.e2e_sharded_cluster_scram_sha_1_upgrade
class TestShardedClusterDeleted(KubernetesTester):
    """
    description: |
      Deletes the Sharded Cluster
    delete:
      file: sharded-cluster-scram-sha-1.yaml
      wait_until: mongo_resource_deleted
      timeout: 240
    """

    def test_noop(self):
        pass
