import pytest

from kubetester.kubetester import KubernetesTester
from kubetester.omtester import get_rs_cert_names

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

        KubernetesTester.wait_until(KubernetesTester.in_running_state)

    # TODO: use /mongodb-automation/server.pem but doesn't exist on test pod
    # def test_mdb_is_reachable(self):
    #     mongo_tester = ReplicaSetTester(mdb_resource, 3, ssl=True)
    #     mongo_tester.assert_connectivity()
