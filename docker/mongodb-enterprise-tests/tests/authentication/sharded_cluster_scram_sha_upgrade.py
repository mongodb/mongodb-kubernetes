import pytest

from kubetester.kubetester import KubernetesTester
from kubetester.mongotester import ShardedClusterTester
from kubetester.automation_config_tester import AutomationConfigTester

MDB_RESOURCE = "my-sharded-cluster-scram-sha-1"


@pytest.mark.e2e_sharded_cluster_scram_sha_1_upgrade
class TestCreateScramSha1ShardedCluster(KubernetesTester):
    """
    description: |
      Creates a ShardedCluster with SCRAM-SHA-1 authentication
    create:
      file: sharded-cluster-scram-sha-1.yaml
      wait_until: in_running_state
    """

    def test_assert_connectivity(self):
        ShardedClusterTester(MDB_RESOURCE, 2).assert_connectivity()

    def test_ops_manager_state_updated_correctly(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_enabled("MONGODB-CR")
        tester.assert_authentication_enabled()

        tester.assert_expected_users(2)
        tester.assert_authoritative_set(True)


@pytest.mark.e2e_sharded_cluster_scram_sha_1_upgrade
class TestUpgradeShardedClusterToMongoDB40(KubernetesTester):
    """
    description: |
      Upgraded the version of MongoDB to 4.0, since MONGODB-CR was enabled previously,
      the deployment should stay at MONGODB-CR and not upgrade to SCRAM-SHA-256
      Note that this operation can take a very long time. Changing authentication in a sharded cluster
      takes a long time, and this step configures authentication multiple times
    update:
      file: sharded-cluster-scram-sha-1.yaml
      patch: '[{"op":"replace","path":"/spec/version", "value": "4.0.1"}]'
      wait_until: in_running_state
    """

    def test_assert_connectivity(self):
        ShardedClusterTester(MDB_RESOURCE, 2).assert_connectivity()

    def test_ops_manager_state_updated_correctly(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_enabled("MONGODB-CR")
        tester.assert_authentication_mechanism_disabled("SCRAM-SHA-256")
        tester.assert_authentication_enabled()

        tester.assert_expected_users(2)
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
