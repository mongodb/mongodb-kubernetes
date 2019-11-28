import pytest

from kubetester.kubetester import KubernetesTester, skip_if_local
from kubernetes import client
from kubetester.mongotester import ReplicaSetTester

MDB_RESOURCE = "test-tls-base-rs-allow-ssl"


def cert_names(namespace):
    return ["{}-{}.{}".format(MDB_RESOURCE, i, namespace) for i in range(3)]


@pytest.mark.e2e_replica_set_tls_allow
class TestReplicaSetWithTLSCreation(KubernetesTester):
    """
    name: Replica Set With TLS Creation
    description: |
      Creates a MongoDB object with the ssl attribute on. The MongoDB object will go to Pending
      state because of missing certificates.
    create:
      file: test-tls-base-rs-allow-ssl.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
      timeout: 120
    """

    def test_mdb_resource_status_is_pending(self):
        assert KubernetesTester.get_resource()["status"]["phase"] == "Pending"


@pytest.mark.e2e_replica_set_tls_allow
class TestReplicaSetWithTLSCreationApproval(KubernetesTester):
    """
    name: Approval of certificates
    description: |
      Approves the certificates in Kubernetes, the MongoDB resource should move to Successful state.
    """

    def setup(self):
        [self.approve_certificate(cert) for cert in cert_names(self.namespace)]

    def test_noop(self):
        assert True


@pytest.mark.e2e_replica_set_tls_allow
class TestReplicaSetWithTLSCreationRunning(KubernetesTester):
    """
    name: check everything is in place
    noop:
      wait_until: in_running_state
      timeout: 200
    """

    @skip_if_local()
    def test_mdb_is_reachable_with_no_ssl(self):
        mongo_tester = ReplicaSetTester(MDB_RESOURCE, 3)
        mongo_tester.assert_connectivity()

    @skip_if_local()
    def test_mdb_is_reachable_with_ssl(self):
        mongo_tester = ReplicaSetTester(MDB_RESOURCE, 3, ssl=True)
        mongo_tester.assert_connectivity()


@pytest.mark.e2e_replica_set_tls_allow
class TestReplicaSetWithTLSCreationRemove(KubernetesTester):
    """
    name: Removal of TLS enabled Replica Set
    delete:
      file: test-tls-base-rs-allow-ssl.yaml
      wait_until: mongo_resource_deleted
    """

    def setup(self):
        # Deletes the certificate
        body = client.V1DeleteOptions()
        [
            self.certificates.delete_certificate_signing_request(name, body=body)
            for name in cert_names(self.namespace)
        ]

    def test_deletion(self):
        assert True
