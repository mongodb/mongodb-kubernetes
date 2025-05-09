import pytest
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongotester import ShardedClusterTester

MDB_RESOURCE = "oidc-sharded-cluster-replica-set"


@pytest.mark.e2e_sharded_cluster_oidc_m2m
class TestCreateOIDCShardedCluster(KubernetesTester):
    """
    description: |
      Creates a ShardedCluster with OIDC M2M authentication
    """

    def test_create_sharded_cluster(self, custom_mdb_version: str):
        resource = MongoDB.from_yaml(load_fixture("oidc/sharded-cluster-replica-set.yaml"), namespace=self.namespace)
        resource.set_version(ensure_ent_version(custom_mdb_version))
        resource.update()

        resource.assert_reaches_phase(Phase.Running)

    def test_assert_connectivity(self):
        tester = ShardedClusterTester(MDB_RESOURCE, 2)
        tester.assert_oidc_authentication()

    def test_ops_manager_state_updated_correctly(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_enabled("MONGODB-OIDC", active_auth_mechanism=False)
        tester.assert_authentication_enabled(2)
        tester.assert_oidc_providers_size(1)
        tester.assert_expected_users(0)
        tester.assert_authoritative_set(True)
