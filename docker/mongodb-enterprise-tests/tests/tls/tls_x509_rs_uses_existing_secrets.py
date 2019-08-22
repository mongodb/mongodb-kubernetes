import pytest
from kubernetes import client
from kubetester.kubetester import KubernetesTester, build_list_of_hosts
from kubetester.omtester import get_rs_cert_names, get_agent_cert_names
from kubetester.mongotester import ReplicaSetTester

mdb_resource = "test-tls-base-rs-require-ssl"

@pytest.mark.e2e_tls_x509_rs_uses_existing_secrets
class TestEnableX509(KubernetesTester):
    @classmethod
    def setup_env(cls):
        cls.patch_config_map(cls.get_namespace(), "my-project", {"authenticationMode": "x509", "credentials": "my-credentials"})

    def test_approve_agent_certs(self):
        for cert in self.yield_existing_csrs(get_agent_cert_names(self.get_namespace())):
            self.approve_certificate(cert)


@pytest.mark.e2e_tls_x509_rs_uses_existing_secrets
class TestReplicaSetWithNoTLSCreation(KubernetesTester):
    """
    create:
      file: test-tls-base-rs-require-ssl.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
      timeout: 60
    """

    def test_noop(self):
        assert True

@pytest.mark.e2e_tls_x509_rs_uses_existing_secrets
class TestApproveTlsCertificates(KubernetesTester):

    def setup(self):
        for cert in self.yield_existing_csrs(get_rs_cert_names(mdb_resource, self.namespace)):
            self.approve_certificate(cert)

    def test_enable_x509(self):
        KubernetesTester.wait_until(KubernetesTester.in_running_state)


@pytest.mark.e2e_tls_x509_rs_uses_existing_secrets
class TestReplicaSetDisableX509(KubernetesTester):
    def setup(self):
        body = client.V1DeleteOptions()
        certs = get_rs_cert_names(mdb_resource, self.namespace, with_agent_certs=True)
        for name in certs:
            self.certificates.delete_certificate_signing_request(name, body=body)

    def test_delete_csrs(self):
        assert True


@pytest.mark.e2e_tls_x509_rs_uses_existing_secrets
class TestDisableX509Authentication(KubernetesTester):
    @classmethod
    def setup_env(cls):
        cls.patch_config_map(cls.get_namespace(), "my-project", {"authenticationMode": "", "credentials": "my-credentials"})

    def test_disable_x509(self):
        KubernetesTester.wait_until(KubernetesTester.in_running_state)


@pytest.mark.e2e_tls_x509_rs_uses_existing_secrets
class TestEnableX509Again(KubernetesTester):
    @classmethod
    def setup_env(cls):
        cls.patch_config_map(cls.get_namespace(), "my-project", {"authenticationMode": "x509", "credentials": "my-credentials"})

    def test_uses_existing_secret(self):  # this operation can take a very long time
         KubernetesTester.wait_until(KubernetesTester.in_running_state)


@pytest.mark.e2e_tls_x509_rs_uses_existing_secrets
class TestNoNewCSRsAreCreated(KubernetesTester):

    @pytest.mark.xfail(reason="operator incorrectly created a new CSR when it should have used the existing secrets")
    def test_no_new_csrs(self):
        cert_names = get_rs_cert_names(mdb_resource, self.namespace, with_agent_certs=True)
        for name in cert_names:
            self.certificates.read_certificate_signing_request(name)

