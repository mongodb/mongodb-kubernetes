from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.operator import Operator


class TestOIDCMultiCluster(KubernetesTester):
    def test_deploy_operator(self, multi_cluster_operator: Operator):
        multi_cluster_operator.assert_is_running()

    def test_create_oidc_replica_set(self, mongodb_multi: MongoDBMulti | MongoDB):
        mongodb_multi.assert_reaches_phase(Phase.Running, timeout=800)

    def test_assert_connectivity(self, mongodb_multi: MongoDBMulti | MongoDB):
        tester = mongodb_multi.tester()
        tester.assert_oidc_authentication()

    def test_ops_manager_state_updated_correctly(self, mongodb_multi: MongoDBMulti | MongoDB):
        tester = mongodb_multi.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled("MONGODB-OIDC", active_auth_mechanism=False)
        tester.assert_authentication_enabled(2)
        tester.assert_expected_users(0)
        tester.assert_authoritative_set(True)
