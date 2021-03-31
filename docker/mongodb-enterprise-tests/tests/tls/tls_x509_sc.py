import pytest
from kubetester.kubetester import KubernetesTester
from kubetester.omtester import get_sc_cert_names

MDB_RESOURCE = "test-x509-sc"


@pytest.mark.e2e_tls_x509_sc
class TestClusterWithTLSCreation(KubernetesTester):
    """
    name: Sharded Cluster With TLS Creation
    description: |
      Creates a MongoDB object with the ssl attribute on. The MongoDB object will go to Failed
      state because of missing certificates.
    create:
      file: test-x509-sc.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
      timeout: 120
    """

    def test_approve_certificates(self):
        for cert in self.yield_existing_csrs(
            get_sc_cert_names(MDB_RESOURCE, self.get_namespace(), with_agent_certs=True)
        ):
            self.approve_certificate(cert)
        KubernetesTester.wait_until("in_running_state")


@pytest.mark.e2e_tls_x509_sc
class TestShardedClusterWithTLSDeletion(KubernetesTester):
    """
    delete:
      file: test-x509-sc.yaml
      wait_until: mongo_resource_deleted_no_om
      timeout: 240
    """

    def test_deletion(self):
        assert True
