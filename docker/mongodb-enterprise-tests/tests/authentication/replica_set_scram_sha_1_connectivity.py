import pytest
from kubetester.mongotester import ReplicaSetTester
from pytest import fixture
from tests.authentication.sha1_connectivity_tests import SHA1ConnectivityTests


@pytest.mark.e2e_replica_set_scram_sha_1_user_connectivity
class TestReplicaSetSHA1Connectivity(SHA1ConnectivityTests):
    @fixture
    def yaml_file(self):
        return "replica-set-explicit-scram-sha-1.yaml"

    @fixture
    def mdb_resource_name(self):
        return "replica-set-scram-sha-1"

    @fixture
    def mongo_tester(self, mdb_resource_name: str):
        return ReplicaSetTester(mdb_resource_name, 3)
