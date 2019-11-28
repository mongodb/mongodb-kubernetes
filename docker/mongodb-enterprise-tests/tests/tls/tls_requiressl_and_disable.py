import pytest
from kubernetes import client
from kubetester.kubetester import KubernetesTester, skip_if_local
from kubetester.mongotester import ReplicaSetTester

MDB_RESOURCE = "test-tls-base-rs-require-ssl"


def cert_names(namespace):
    return ["{}-{}.{}".format(MDB_RESOURCE, i, namespace) for i in range(3)]


@pytest.mark.e2e_replica_set_tls_require_and_disable
class TestReplicaSetWithTLSCreation(KubernetesTester):
    """
    name: Replica Set With TLS Creation
    description: |
      Creates a MongoDB object with the ssl attribute on. The MongoDB object will go to Pending
      state because of missing certificates.
    create:
      file: test-tls-base-rs-require-ssl.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
      timeout: 240
    """

    def test_mdb_resource_status_is_pending(self):
        assert KubernetesTester.get_resource()["status"]["phase"] == "Pending"


@pytest.mark.e2e_replica_set_tls_require_and_disable
class TestReplicaSetWithTLSApproval(KubernetesTester):
    """
    name: Approval of certificates
    description: |
      Approves the certificates in Kubernetes, the MongoDB resource should move to Successful state.
    """

    def test_approve_certs(self):
        [self.approve_certificate(cert) for cert in cert_names(self.namespace)]


@pytest.mark.e2e_replica_set_tls_require_and_disable
class TestReplicaSetWithTLSRunning(KubernetesTester):
    """
    name: check everything is in place
    noop:
      wait_until: in_running_state
      timeout: 300
    """

    @skip_if_local()
    def test_mdb_is_not_reachable_with_no_ssl(self):
        ReplicaSetTester(MDB_RESOURCE, 3).assert_no_connection()

    @skip_if_local()
    def test_mdb_is_reachable_with_ssl(self):
        ReplicaSetTester(MDB_RESOURCE, 3, ssl=True).assert_connectivity()


@pytest.mark.e2e_replica_set_tls_require_and_disable
class TestReplicaSetWithTLSPrefer(KubernetesTester):
    """
    name: Change ssl configuration to preferSSL
    update:
      patch: '[{"op":"add","path":"/spec/additionalMongodConfig","value": { "net": {"ssl": {"mode": "preferSSL"}} }}]'
      file: test-tls-base-rs-require-ssl.yaml
      wait_until: in_running_state
      timeout: 300
    """

    @skip_if_local()
    def test_mdb_is_reachable_with_no_ssl(self):
        ReplicaSetTester(MDB_RESOURCE, 3).assert_connectivity()

    @skip_if_local()
    def test_mdb_is_reachable_with_ssl(self):
        ReplicaSetTester(MDB_RESOURCE, 3, ssl=True).assert_connectivity()


@pytest.mark.e2e_replica_set_tls_require_and_disable
class TestReplicaSetWithTLSAllow(KubernetesTester):
    """
    name: Change ssl configuration to allowSSL
    update:
      patch: '[{"op":"add","path":"/spec/additionalMongodConfig","value": { "net": {"ssl": {"mode": "allowSSL"}} }}]'
      file: test-tls-base-rs-require-ssl.yaml
      wait_until: in_running_state
      timeout: 300
    """

    @skip_if_local()
    def test_mdb_is_reachable_with_no_ssl(self):
        ReplicaSetTester(MDB_RESOURCE, 3).assert_connectivity()

    @skip_if_local()
    def test_mdb_is_reachable_with_ssl(self):
        ReplicaSetTester(MDB_RESOURCE, 3, ssl=True).assert_connectivity()


@pytest.mark.e2e_replica_set_tls_require_and_disable
class TestReplicaSetWithTLSDisabling(KubernetesTester):
    """
    name: check everything is in place
    update:
      patch: '[{ "op": "replace", "path":"/spec/security", "value": null }]'
      file: test-tls-base-rs-require-ssl.yaml
      wait_until: in_running_state
      timeout: 300
    """

    @skip_if_local()
    def test_mdb_is_reachable_with_no_ssl(self):
        ReplicaSetTester(MDB_RESOURCE, 3).assert_connectivity()

    @skip_if_local()
    def test_mdb_is_not_reachable_with_ssl(self):
        ReplicaSetTester(MDB_RESOURCE, 3, ssl=True).assert_no_connection()


@pytest.mark.e2e_replica_set_tls_require_and_disable
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
