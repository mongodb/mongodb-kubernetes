import pytest
from kubetester.mongotester import ReplicaSetTester, ShardedClusterTester
from pytest import fixture
from tests.static.static_migration_tests import MigrationConnectivityTests


@pytest.mark.e2e_sharded_cluster_migration
class TestShardedClusterMigrationStatic(MigrationConnectivityTests):
    @fixture(scope="module")
    def yaml_file(self):
        return "sharded-cluster.yaml"

    @fixture(scope="module")
    def mdb_resource_name(self):
        return "sharded-cluster-migration"

    @fixture(scope="module")
    def mongo_tester(self, mdb_resource_name: str):
        return ShardedClusterTester(mdb_resource_name, 1)
