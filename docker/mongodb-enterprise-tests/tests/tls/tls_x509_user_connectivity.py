import time
import base64
import tempfile
import ssl

import pytest
import pymongo
from kubetester.kubetester import KubernetesTester, fixture
from kubetester.omtester import get_agent_cert_names, get_rs_cert_names
from kubetester.mongotester import build_mongodb_connection_uri

mdb_resource = "test-tls-base-rs-require-ssl"


@pytest.mark.e2e_tls_x509_user_connectivity
class TestsReplicaSetWithNoTLSWithX509Project(KubernetesTester):
    def test_enable_x509(self):
        self.patch_config_map(self.get_namespace(), "my-project", {"authenticationMode": "x509", "credentials": "my-credentials"})

@pytest.mark.e2e_tls_x509_user_connectivity
class TestReplicaSetWithTLSCreation(KubernetesTester):
    """
    name: Replica Set With TLS Creation
    description: |
      Creates a MongoDB object with the ssl attribute on. The MongoDB object will go to Failed
      state because of missing certificates.
    create:
      file: test-tls-base-rs-require-ssl.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
    """

    def test_mdb_resource_status_is_correct(self):
        assert True


@pytest.mark.e2e_tls_x509_user_connectivity
class TestReplicaSetWithTLSRunning(KubernetesTester):
    def test_approve_certs(self):
        for cert in self.yield_existing_csrs(get_rs_cert_names(mdb_resource, self.get_namespace(), with_agent_certs=True)):
            self.approve_certificate(cert)
        KubernetesTester.wait_until('in_running_state', 240)
        print('finished waiting')


@pytest.mark.e2e_tls_x509_user_connectivity
class TestAddMongoDBUser(KubernetesTester):
    """
    create:
      file: test-tls-x509-user.yaml
      wait_until: user_exists
    """

    def test_add_user(self):
        assert True

    @staticmethod
    def user_exists():
        ac = KubernetesTester.get_automation_config()
        users = ac['auth']['usersWanted']
        return 'CN=x509-testing-user' in [user['user'] for user in users]


@pytest.mark.e2e_tls_x509_user_connectivity
class TestX509CertCreationAndApproval(KubernetesTester):
    def setup(self):
        cert_name = f'x509-testing-user.{self.get_namespace()}'
        with open(fixture('x509-testing-user.csr'), "r") as f:
            encoded_request = base64.b64encode(f.read().encode('utf-8')).decode('utf-8')
        csr_body = self.client.V1beta1CertificateSigningRequest(
            metadata=self.client.V1ObjectMeta(
                name=cert_name,
                namespace=self.namespace,
            ),
            spec=self.client.V1beta1CertificateSigningRequestSpec(
                groups=["system:authenticated"],
                usages=["digital signature", "key encipherment", "client auth"],
                request=encoded_request
            )
        )
        self.certificates.create_certificate_signing_request(csr_body)

        self.approve_certificate(cert_name)
        time.sleep(10)  # FIXME: waits for cert to be approved

        csr = self.certificates.read_certificate_signing_request(cert_name)
        cert = base64.b64decode(csr.status.certificate)

        self.cert_file = tempfile.NamedTemporaryFile()
        with open(fixture('server-key.pem'), 'r+b') as f:
            key = f.read()
        self.cert_file.write(key)
        self.cert_file.write(cert)
        self.cert_file.flush()

    def teardown(self):
        self.cert_file.close()

    def test_can_authenticate_with_added_user(self):
        time.sleep(20)
        uri = build_mongodb_connection_uri(mdb_resource, self.namespace, 3)

        conn = pymongo.MongoClient(
            uri,
            authMechanism="MONGODB-X509",
            ssl=True,
            ssl_certfile=self.cert_file.name,
            ssl_cert_reqs=ssl.CERT_REQUIRED,
            ssl_ca_certs='/var/run/secrets/kubernetes.io/serviceaccount/ca.crt'
        )
        successfully_authenticated = conn.admin.authenticate(
            "CN=x509-testing-user",
            mechanism='MONGODB-X509'
        )
        assert successfully_authenticated
