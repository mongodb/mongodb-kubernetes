import re
import pytest
from kubetester.omtester import get_rs_cert_names
from kubetester.kubetester import KubernetesTester, skip_if_local
from kubetester.mongotester import ReplicaSetTester

MDB_RESOURCE_NAME = "test-tls-additional-domains"


@pytest.mark.e2e_tls_rs_additional_certs
class TestReplicaSetWithAdditionalCertDomains(KubernetesTester):
    """
    create:
      file: test-tls-additional-domains.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
      timeout: 240
    """

    def test_certs_generated(self):
        csr_names = get_rs_cert_names(MDB_RESOURCE_NAME, self.namespace)
        for csr_name in self.yield_existing_csrs(csr_names):
            self.approve_certificate(csr_name)
        self.wait_until("in_running_state")

    @skip_if_local
    def test_has_right_certs(self):
        """
        Check that mongod processes behind the replica set service are
        serving the right certificates.
        """
        for i in range(3):
            host = (
                f"{MDB_RESOURCE_NAME}-{i}.{MDB_RESOURCE_NAME}-svc.{self.namespace}.svc"
            )
            assert any(
                re.match(fr"{MDB_RESOURCE_NAME}-{i}\.additional-cert-test\.com", san)
                for san in self.get_mongo_server_sans(host)
            )

    @skip_if_local
    def test_can_still_connect(self):
        tester = ReplicaSetTester(MDB_RESOURCE_NAME, 3, ssl=True)
        tester.assert_connectivity()


@pytest.mark.e2e_tls_rs_additional_certs
class TestReplicaSetRemoveAdditionalCertDomains(KubernetesTester):
    """
    update:
      file: test-tls-additional-domains.yaml
      wait_until: in_running_state
      patch: '[{"op":"remove","path":"/spec/security/tls/additionalCertificateDomains"}]'
      timeout: 240
    """

    def test_continues_to_work(self):
        pass

    @skip_if_local
    def test_can_still_connect(self):
        tester = ReplicaSetTester(MDB_RESOURCE_NAME, 3, ssl=True)
        tester.assert_connectivity()
