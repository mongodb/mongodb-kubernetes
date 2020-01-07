import pytest

from kubetester.kubetester import KubernetesTester
from kubetester.mongotester import StandaloneTester
from kubetester.omtester import get_st_cert_names

MDB_RESOURCE = "my-standalone"


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_st
class TestCreateReplicaSet(KubernetesTester):
    """
    create:
      file: standalone.yaml
      wait_until: in_running_state
      timeout: 240
    """

    def test_connectivity(self):
        tester = StandaloneTester(MDB_RESOURCE)
        tester.assert_connectivity()


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_st
class TestEnableX509AndTls(KubernetesTester):
    """
    update:
      file: standalone.yaml
      patch: '[{"op":"add","path":"/spec/security", "value" : {"authentication" : {"enabled" : true, "modes" : ["X509"]}, "tls" : { "enabled" : true }}} ]'
      wait_for_message: Not all certificates have been approved by Kubernetes CA
    """

    def test_approve_certificates(self):
        for cert in self.yield_existing_csrs(
            get_st_cert_names(MDB_RESOURCE, self.namespace, with_agent_certs=True)
        ):
            self.approve_certificate(cert)
        KubernetesTester.wait_until(KubernetesTester.in_running_state, timeout=900)


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_st
class TestStandaloneDeleted(KubernetesTester):
    """
    description: |
      Deletes the Standalone
    delete:
      file: standalone.yaml
      wait_until: mongo_resource_deleted
      timeout: 240
    """

    def test_noop(self):
        pass
