import pytest
from kubetester.mongotester import ShardedClusterTester
from pytest import fixture
from tests.authentication.sha1_connectivity_tests import SHA1ConnectivityTests


@pytest.mark.e2e_sharded_cluster_scram_sha_1_user_connectivity
class TestShardedClusterSHA1Connectivity(SHA1ConnectivityTests):
    @fixture
    def yaml_file(self):
        return "sharded-cluster-explicit-scram-sha-1.yaml"

    @fixture
    def mdb_resource_name(self):
        return "my-sharded-cluster-scram-sha-1"

    @fixture
    def mongo_tester(self, mdb_resource_name: str):
        return ShardedClusterTester(mdb_resource_name, 2)
