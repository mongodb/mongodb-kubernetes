import pytest

from kubetester.kubetester import (
    KubernetesTester,
    SERVER_WARNING,
    AGENT_WARNING,
    MEMBER_AUTH_WARNING,
)
from kubetester.omtester import get_rs_cert_names
from kubetester.automation_config_tester import AutomationConfigTester

MDB_RESOURCE = "test-x509-all-options-rs"


@pytest.mark.e2e_tls_x509_configure_all_options_rs
class TestReplicaSetEnableAllOptions(KubernetesTester):
    """
    create:
      file: test-x509-all-options-rs.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
      timeout: 240
    """

    def test_approve_certs(self):
        for cert in self.yield_existing_csrs(
            get_rs_cert_names(
                MDB_RESOURCE,
                self.namespace,
                with_internal_auth_certs=True,
                with_agent_certs=True,
            )
        ):
            self.approve_certificate(cert)

    def test_gets_to_running_state(self):
        self.wait_until(KubernetesTester.in_running_state, 1200)

    def test_ops_manager_state_correctly_updated(self):
        ac_tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        ac_tester.assert_internal_cluster_authentication_enabled()
        ac_tester.assert_authentication_enabled()

    # TODO refactor the test to use the Operator class instead
    def test_operator_logs_tls_certificate_warnings(self, namespace):
        operator_pod = KubernetesTester.read_operator_pod(namespace)
        logs = KubernetesTester.read_pod_logs(namespace, operator_pod.metadata.name)

        assert SERVER_WARNING in logs
        assert AGENT_WARNING in logs
        assert MEMBER_AUTH_WARNING in logs

    # TODO: use /mongodb-automation/server.pem but doesn't exist on test pod
    # def test_mdb_is_reachable(self):
    #     mongo_tester = ReplicaSetTester(mdb_resource, 3, ssl=True)
    #     mongo_tester.assert_connectivity()
