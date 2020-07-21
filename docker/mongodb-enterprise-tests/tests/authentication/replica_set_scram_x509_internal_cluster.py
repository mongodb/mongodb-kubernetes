from kubetester.kubetester import KubernetesTester
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.omtester import get_rs_cert_names
from pytest import mark

MDB_RESOURCE = "my-replica-set"


@mark.e2e_replica_set_scram_x509_internal_cluster
class TestReplicaSetScramX509Internal(KubernetesTester):
    """
    create:
        file: replica-set-scram-sha-256-x509-internal-cluster.yaml
        wait_for_message: Not all certificates have been approved by Kubernetes CA
        timeout: 240
    """

    def test_approve_internal_cluster_certs(self):
        for cert in self.yield_existing_csrs(
            get_rs_cert_names(
                MDB_RESOURCE,
                KubernetesTester.get_namespace(),
                with_agent_certs=False,
                with_internal_auth_certs=True,
            )
        ):
            self.approve_certificate(cert)

    def test_wait_until_running(self):
        KubernetesTester.wait_until(KubernetesTester.in_running_state, timeout=1200)

    def test_ops_manager_state_was_updated_correctly(self):
        ac_tester = AutomationConfigTester(self.get_automation_config())
        ac_tester.assert_authentication_enabled()
        ac_tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
        ac_tester.assert_expected_users(2)
        ac_tester.assert_authoritative_set(True)
        ac_tester.assert_internal_cluster_authentication_enabled()
