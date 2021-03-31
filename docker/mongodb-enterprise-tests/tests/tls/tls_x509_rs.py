import pytest
from kubetester.kubetester import KubernetesTester, skip_if_local
from kubetester.omtester import get_rs_cert_names

from kubetester.mongotester import ReplicaSetTester

MDB_RESOURCE = "test-x509-rs"


@pytest.mark.e2e_tls_x509_rs
class TestReplicaSetWithNoTLSCreation(KubernetesTester):
    """
    create:
      file: test-x509-rs.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
      timeout: 240
    """

    def test_approve_certs(self):
        for cert in self.yield_existing_csrs(
            get_rs_cert_names(MDB_RESOURCE, self.namespace, with_agent_certs=True)
        ):
            print("Approving certificate {}".format(cert))
            self.approve_certificate(cert)
        KubernetesTester.wait_until("in_running_state")

    @skip_if_local
    def test_mdb_is_reachable_with_no_ssl(self):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_no_connection()

    @skip_if_local
    def test_mdb_is_reachable_with_ssl(self):
        # This one will also fails, as it expects x509 client certs which we are not passing.
        tester = ReplicaSetTester(MDB_RESOURCE, 3, ssl=True)
        tester.assert_no_connection()


@pytest.mark.e2e_tls_x509_rs
class TestReplicaSetWithNoTLSDeletion(KubernetesTester):
    """
    delete:
      file: test-x509-rs.yaml
      wait_until: mongo_resource_deleted_no_om
      timeout: 240
    """

    def test_deletion(self):
        assert True
