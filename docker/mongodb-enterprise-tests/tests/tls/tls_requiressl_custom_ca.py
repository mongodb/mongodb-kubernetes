import pytest

from kubernetes import client
from kubetester.kubetester import KubernetesTester, skip_if_local
from kubetester.crypto import generate_csr, request_certificate, get_pem_certificate
from kubetester.mongotester import ReplicaSetTester

from typing import Dict

mdb_resource = "test-tls-base-rs-require-ssl"


def cert_names(namespace, members=3):
    return ["{}-{}.{}".format(mdb_resource, i, namespace) for i in range(members)]


@pytest.mark.e2e_replica_set_tls_require_custom_ca
class TestReplicaSetCreateCerts(KubernetesTester):
    @classmethod
    def setup_env(cls):
        usages = ["digital signature", "key encipherment", "server auth", "client auth"]
        cls.keys = {}
        for i in range(3):
            pod_name = f"{mdb_resource}-{i}"
            csr, key = generate_csr(cls.get_namespace(), pod_name, f"{mdb_resource}-svc")
            cls.keys[pod_name] = key
            request_certificate(csr, "{}.{}".format(pod_name, cls.get_namespace()), usages)

        # Creates the "customer-ca" with an existing CA.
        data = cls.read_configmap("default", "ca-certificates")
        cls.create_config_map(cls.get_namespace(), "customer-ca", data)

    def test_approve_certs(self):
        for cert in self.yield_existing_csrs(cert_names(self.get_namespace(), 3)):
            self.approve_certificate(cert)

    def test_create_secrets(self):
        server_certs: Dict[str, [bytes]] = dict()
        for i in range(3):
            pod_name = f"{mdb_resource}-{i}"
            cert = get_pem_certificate("{}.{}".format(pod_name, self.get_namespace()))
            server_certs[f"{pod_name}-pem"] = (cert + self.keys[pod_name]).decode("utf-8")

        KubernetesTester.create_secret(self.get_namespace(), f"{mdb_resource}-cert", server_certs)

    def test_remove_csr(self):
        body = client.V1DeleteOptions()
        for cert in cert_names(self.get_namespace()):
            self.certificates.delete_certificate_signing_request(cert, body=body)


@pytest.mark.e2e_replica_set_tls_require_custom_ca
class TestReplicaSetWithTLSCreation(KubernetesTester):
    """
    name: Replica Set With TLS Creation
    description: |
      Creates a MongoDB object with the ssl attribute on. The MongoDB object will go to Running,
      because the Secrets with the certificates have been created already.
    create:
      file: test-tls-base-rs-require-ssl-custom-ca.yaml
      wait_until: in_running_state
    """

    def test_mdb_resource_status_is_running(self):
        assert KubernetesTester.get_resource()['status']['phase'] == "Running"


@pytest.mark.e2e_replica_set_tls_require_custom_ca
class TestReplicaSetWithTLSRunning(KubernetesTester):
    """
    name: MDB object works with 3 nodes approved
    noop:
      wait_until: in_running_state
    """

    @skip_if_local()
    def test_mdb_is_not_reachable_with_no_ssl(self):
        mongo_tester = ReplicaSetTester(mdb_resource, 3)
        mongo_tester.assert_no_connection()

    @skip_if_local()
    def test_mdb_is_reachable_with_ssl(self):
        mongo_tester = ReplicaSetTester(mdb_resource, 3, ssl=True)
        mongo_tester.assert_connectivity()


@pytest.mark.e2e_replica_set_tls_require_custom_ca
class TestReplicaSetAddCerts(KubernetesTester):
    @classmethod
    def setup_env(cls):
        usages = ["digital signature", "key encipherment", "server auth", "client auth"]
        cls.keys = {}
        for i in range(3, 5):
            pod_name = f"{mdb_resource}-{i}"
            csr, key = generate_csr(cls.get_namespace(), pod_name, f"{mdb_resource}-svc")
            cls.keys[pod_name] = key
            request_certificate(csr, "{}.{}".format(pod_name, cls.get_namespace()), usages)

    def test_approve_certs(self):
        certificates = cert_names(self.get_namespace(), 5)[3:]
        for cert in self.yield_existing_csrs(certificates):
            self.approve_certificate(cert)

    def test_update_secrets(self):
        server_certs: Dict[str, [bytes]] = dict()
        for i in range(3, 5):
            pod_name = f"{mdb_resource}-{i}"
            cert = get_pem_certificate("{}.{}".format(pod_name, self.get_namespace()))
            server_certs[f"{pod_name}-pem"] = (cert + self.keys[pod_name]).decode("utf-8")

        KubernetesTester.update_secret(self.get_namespace(), f"{mdb_resource}-cert", server_certs)


@pytest.mark.e2e_replica_set_tls_require_custom_ca
class TestReplicaSetWithTLSScaling1(KubernetesTester):
    """
    name: After scaling to 5, the Replica Set is still working
    update:
      patch: '[{"op":"replace","path":"/spec/members","value": 5}]'
      file: test-tls-base-rs-require-ssl-custom-ca.yaml
      wait_until: in_running_state
    """

    def test_noop(self):
        assert True

    @skip_if_local()
    def test_mdb_is_reachable_with_no_ssl(self):
        mongo_tester = ReplicaSetTester(mdb_resource, 5)
        mongo_tester.assert_no_connection()

    @skip_if_local()
    def test_mdb_is_reachable_with_ssl(self):
        mongo_tester = ReplicaSetTester(mdb_resource, 5, ssl=True)
        mongo_tester.assert_connectivity()


@pytest.mark.e2e_replica_set_tls_require_custom_ca
class TestReplicaSetWithTLSRemove(KubernetesTester):
    """
    name: Removal of TLS enabled Replica Set
    description: |
      Removes TLS enabled Replica Set
    delete:
      file: test-tls-base-rs-require-ssl-custom-ca.yaml
      wait_until: mongo_resource_deleted
    """

    def test_deletion(self):
        self.delete_configmap(self.get_namespace(), "customer-ca")
        self.delete_secret(self.get_namespace(), f"{mdb_resource}-cert")
