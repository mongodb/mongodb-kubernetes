from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB
from kubetester.mongotester import ReplicaSetTester
from pytest import fixture, mark
from tests.authentication.sha1_connectivity_tests import (
    SHA1ConnectivityTests,
    run_authentication_disabled_after_resource_deleted,
)


@mark.e2e_replica_set_scram_sha_1_user_connectivity
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

    def test_ops_manager_state_correctly_updated(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_enabled("MONGODB-CR")
        tester.assert_authoritative_set(True)
        tester.assert_authentication_enabled(3)
        tester.assert_expected_users(0)

    def test_authentication_is_disabled_once_resource_is_deleted(self, mdb: MongoDB):
        run_authentication_disabled_after_resource_deleted(mdb)
