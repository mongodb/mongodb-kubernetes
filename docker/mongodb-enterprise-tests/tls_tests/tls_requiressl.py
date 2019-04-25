import pytest

import sys
import os
import os.path

try:
    from kubetester import KubernetesTester, skip_if_local, build_list_of_hosts
except ImportError:
    # patching python import path so it finds kubetester
    sys.path.append(os.path.dirname(os.getcwd()))
    from kubetester import KubernetesTester, skip_if_local, build_list_of_hosts
from kubernetes import client


mdb_resource = "test-tls-base-rs-require-ssl"


def cert_names(namespace, members=3):
    return ["{}-{}.{}".format(mdb_resource, i, namespace) for i in range(members)]


@pytest.mark.tls_base_require
class TestReplicaSetWithTLSCreation(KubernetesTester):
    """
    name: Replica Set With TLS Creation
    description: |
      Creates a MongoDB object with the ssl attribute on. The MongoDB object will go to Failed
      state because of missing certificates.
    create:
      file: fixtures/test-tls-base-rs-require-ssl.yaml
      wait_until: in_failed_state
      timeout: 120
    """

    def test_mdb_resource_status_is_correct(self):
        mdb = self.customv1.get_namespaced_custom_object(
            "mongodb.com", "v1", self.namespace, "mongodb", mdb_resource
        )
        assert (
            mdb["status"]["message"]
            == "Not all certificates have been approved by Kubernetes CA"
        )


@pytest.mark.tls_base_require
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


@pytest.mark.tls_base_require
class TestReplicaSetWithTLSRunning(KubernetesTester):
    """
    name: MDB object works with 3 nodes approved
    noop:
      wait_until: in_running_state
      timeout: 200
    """

    @skip_if_local()
    @pytest.mark.xfail
    def test_mdb_is_reachable_with_no_ssl(self):
        hosts = build_list_of_hosts(mdb_resource, self.namespace, 3)
        primary, secondaries = self.wait_for_rs_is_ready(hosts, wait_for=20)

        assert primary is not None
        assert len(secondaries) == 2

    @skip_if_local()
    def test_mdb_is_reachable_with_ssl(self):
        hosts = build_list_of_hosts(mdb_resource, self.namespace, 3)
        primary, secondaries = self.wait_for_rs_is_ready(hosts, ssl=True)

        assert primary is not None
        assert len(secondaries) == 2


@pytest.mark.tls_base_require
class TestReplicaSetWithTLSScaling0Approval(KubernetesTester):
    """
    name: MDB object is scaled to 5 members and it goes to pending state
    update:
      patch: '[{"op":"replace","path":"/spec/members","value":5}]'
      file: fixtures/test-tls-base-rs-require-ssl.yaml
      wait_until: in_failed_state
      timeout: 200
    """
    def setup(self):
        [self.approve_certificate(cert) for cert in cert_names(self.namespace, 5)]

    def test_noop(self):
        assert True


@pytest.mark.tls_base_require
class TestReplicaSetWithTLSScaling0Running(KubernetesTester):
    """
    name: After certs have been approved, the MDB object goes to success
    noop:
      wait_until: in_running_state
      timeout: 200
    """

    def test_noop(self):
        assert True

    @skip_if_local()
    @pytest.mark.xfail
    def test_mdb_is_reachable_with_no_ssl(self):
        hosts = build_list_of_hosts(mdb_resource, self.namespace, 5)
        primary, secondaries = self.wait_for_rs_is_ready(hosts, wait_for=20)

        assert primary is not None
        assert len(secondaries) == 4

    @skip_if_local()
    def test_mdb_is_reachable_with_ssl(self):
        hosts = build_list_of_hosts(mdb_resource, self.namespace, 5)
        primary, secondaries = self.wait_for_rs_is_ready(hosts, ssl=True)

        assert primary is not None
        assert len(secondaries) == 4


@pytest.mark.tls_base_require
class TestReplicaSetWithTLSScaling1(KubernetesTester):
    """
    name: After scaling back to 5, the Replica Set works with no certs approval
    update:
      patch: '[{"op":"replace","path":"/spec/members","value": 3}]'
      file: fixtures/test-tls-base-rs-require-ssl.yaml
      wait_for_condition: sts/test-tls-base-rs-require-ssl -> status.current_replicas == 3
      timeout: 200
    """

    def test_noop(self):
        assert True

    @skip_if_local()
    @pytest.mark.xfail
    def test_mdb_is_reachable_with_no_ssl(self):
        hosts = build_list_of_hosts(mdb_resource, self.namespace, 3)
        primary, secondaries = self.wait_for_rs_is_ready(hosts, wait_for=20)

        assert primary is not None
        assert len(secondaries) == 2

    @skip_if_local()
    def test_mdb_is_reachable_with_ssl(self):
        hosts = build_list_of_hosts(mdb_resource, self.namespace, 3)
        primary, secondaries = self.wait_for_rs_is_ready(hosts, ssl=True)

        assert primary is not None
        assert len(secondaries) == 2


@pytest.mark.tls_base_require
class TestReplicaSetWithTLSRemove(KubernetesTester):
    """
    name: Removal of TLS enabled Replica Set
    description: |
      Removes TLS enabled Replica Set
    delete:
      file: fixtures/test-tls-base-rs-require-ssl.yaml
      wait_until: mongo_resource_deleted
      timeout: 120
    """

    def setup(self):
        # Deletes the certificate
        body = client.V1DeleteOptions()
        [
            self.certificates.delete_certificate_signing_request(name, body)
            for name in cert_names(self.namespace)
        ]

    def test_deletion(self):
        assert True
