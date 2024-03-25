import pytest
from kubetester.mongotester import ReplicaSetTester
from pytest import fixture
from tests.static.static_migration_tests import MigrationConnectivityTests


@pytest.mark.e2e_replica_set_migration
class TestReplicaSetMigrationStatic(MigrationConnectivityTests):
    @fixture(scope="module")
    def yaml_file(self):
        return "replica-set.yaml"

    @fixture(scope="module")
    def mdb_resource_name(self):
        return "replica-set-migration"

    @fixture(scope="module")
    def mongo_tester(self, mdb_resource_name: str):
        return ReplicaSetTester(mdb_resource_name, 3)
