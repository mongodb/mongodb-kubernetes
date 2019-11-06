import pytest

from kubetester.kubetester import KubernetesTester
from kubetester.mongotester import ReplicaSetTester
from kubetester.omtester import get_rs_cert_names

MDB_RESOURCE = "my-replica-set"


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_rs
class TestCreateReplicaSet(KubernetesTester):
    """
    create:
      file: replica-set.yaml
      wait_until: in_running_state
      timeout: 240
    """

    def test_connectivity(self):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_connectivity()


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_rs
class TestEnableX509AndTls(KubernetesTester):
    """
    update:
      file: replica-set.yaml
      patch: '[{"op":"add","path":"/spec/security", "value" : {"authentication" : {"enabled" : true, "modes" : ["X509"]}, "tls" : { "enabled" : true }}} ]'
      wait_for_message: Not all certificates have been approved by Kubernetes CA
    """

    def test_approve_certificates(self):
        for cert in self.yield_existing_csrs(
                get_rs_cert_names(MDB_RESOURCE, self.namespace, with_agent_certs=True)):
            self.approve_certificate(cert)
        KubernetesTester.wait_until(KubernetesTester.in_running_state)


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_rs
class TestReplicaSetDeleted(KubernetesTester):
    """
    description: |
      Deletes the Replica Set
    delete:
      file: replica-set.yaml
      wait_until: mongo_resource_deleted
      timeout: 240
    """

    def test_noop(self):
        pass
