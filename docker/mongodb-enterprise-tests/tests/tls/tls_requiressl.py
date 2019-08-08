import pytest

from kubetester.kubetester import KubernetesTester, skip_if_local
from kubernetes import client
from kubetester.mongotester import ReplicaSetTester

mdb_resource = "test-tls-base-rs-require-ssl"


def cert_names(namespace, members=3):
    return ["{}-{}.{}".format(mdb_resource, i, namespace) for i in range(members)]


@pytest.mark.e2e_replica_set_tls_require
class TestReplicaSetWithTLSCreation(KubernetesTester):
    """
    name: Replica Set With TLS Creation
    description: |
      Creates a MongoDB object with the ssl attribute on. The MongoDB object will go to Pending
      state because of missing certificates.
    create:
      file: test-tls-base-rs-require-ssl.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
      timeout: 120
    """

    def test_mdb_resource_status_is_pending(self):
        assert KubernetesTester.get_resource()['status']['phase'] == "Pending"


@pytest.mark.e2e_replica_set_tls_require
class TestReplicaSetWithTLSApproval(KubernetesTester):
    """
    name: Approval of certificates
    description: |
      Approves the certificates in Kubernetes, the MongoDB resource should move to Successful state.
    """

    def setup(self):
        [self.approve_certificate(cert) for cert in cert_names(self.namespace)]

    def test_noop(self):
        assert True


@pytest.mark.e2e_replica_set_tls_require
class TestReplicaSetWithTLSRunning(KubernetesTester):
    """
    name: MDB object works with 3 nodes approved
    noop:
      wait_until: in_running_state
      timeout: 300
    """

    @skip_if_local()
    def test_mdb_is_not_reachable_with_no_ssl(self):
        mongo_tester = ReplicaSetTester(mdb_resource, 3)
        mongo_tester.assert_no_connection()

    @skip_if_local()
    def test_mdb_is_reachable_with_ssl(self):
        mongo_tester = ReplicaSetTester(mdb_resource, 3, ssl=True)
        mongo_tester.assert_connectivity()


@pytest.mark.e2e_replica_set_tls_require
class TestReplicaSetWithTLSScaling0Approval(KubernetesTester):
    """
    name: MDB object is scaled to 5 members and it goes to pending state
    update:
      patch: '[{"op":"replace","path":"/spec/members","value":5}]'
      file: test-tls-base-rs-require-ssl.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
      timeout: 300
    """

    def setup(self):
        for cert in self.yield_existing_csrs(cert_names(self.namespace, 5)):
            self.approve_certificate(cert)

    def test_noop(self):
        assert True


@pytest.mark.e2e_replica_set_tls_require
class TestReplicaSetWithTLSScaling0Running(KubernetesTester):
    """
    name: After certs have been approved, the MDB object goes to success
    noop:
      wait_until: in_running_state
    """

    def test_noop(self):
        assert True

    @skip_if_local()
    def test_mdb_is_not_reachable_with_no_ssl(self):
        mongo_tester = ReplicaSetTester(mdb_resource, 5)
        mongo_tester.assert_no_connection()

    @skip_if_local()
    def test_mdb_is_reachable_with_ssl(self):
        mongo_tester = ReplicaSetTester(mdb_resource, 5, ssl=True)
        mongo_tester.assert_connectivity()


@pytest.mark.e2e_replica_set_tls_require
class TestReplicaSetWithTLSScaling1(KubernetesTester):
    """
    name: After scaling back to 3, the Replica Set works with no certs approval
    update:
      patch: '[{"op":"replace","path":"/spec/members","value": 3}]'
      file: test-tls-base-rs-require-ssl.yaml
      wait_for_condition: sts/test-tls-base-rs-require-ssl -> status.current_replicas == 3
      timeout: 300
    """

    def test_noop(self):
        assert True

    @skip_if_local()
    def test_mdb_is_reachable_with_no_ssl(self):
        mongo_tester = ReplicaSetTester(mdb_resource, 3)
        mongo_tester.assert_no_connection()

    @skip_if_local()
    def test_mdb_is_reachable_with_ssl(self):
        mongo_tester = ReplicaSetTester(mdb_resource, 3, ssl=True)
        mongo_tester.assert_connectivity()


@pytest.mark.e2e_replica_set_tls_require
class TestReplicaSetWithTLSRemove(KubernetesTester):
    """
    name: Removal of TLS enabled Replica Set
    description: |
      Removes TLS enabled Replica Set
    delete:
      file: test-tls-base-rs-require-ssl.yaml
      wait_until: mongo_resource_deleted
      timeout: 240
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
