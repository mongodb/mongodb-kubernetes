import pytest
from kubernetes import client
from kubetester.kubetester import KubernetesTester, build_list_of_hosts
from kubetester.omtester import get_rs_cert_names

mdb_resource = "test-tls-upgrade"


@pytest.mark.e2e_tls_x509_rs
class TestReplicaSetWithNoTLSCreation(KubernetesTester):
    """
    create:
      file: test-tls-base-rs-require-ssl-upgrade.yaml
      wait_until: in_running_state
      timeout: 240
    """

    def test_mdb_is_reachable_with_no_ssl(self):
        hosts = build_list_of_hosts(mdb_resource, self.namespace, 3)
        primary, secondaries = self.wait_for_rs_is_ready(hosts, wait_for=120)

        assert primary is not None
        assert len(secondaries) == 2


@pytest.mark.e2e_tls_x509_rs
class TestsReplicaSetWithNoTLSWithX509Project(KubernetesTester):
    @classmethod
    def setup_env(cls):
        cls.patch_config_map(cls.get_namespace(), "my-project", {"authenticationMode": "x509", "credentials": "my-credentials"})

    def test_noop(self):
        pass


@pytest.mark.e2e_tls_x509_rs
class TestReplicaSetUpgradeToTLSWithX509Project(KubernetesTester):
    """
    update:
      file: test-tls-base-rs-require-ssl-upgrade.yaml
      patch: '[{"op":"add","path":"/spec/security","value":{"tls": { "enabled": true }}}]'
      wait_for_message: Not all certificates have been approved by Kubernetes CA
      timeout: 240
    """

    def test_mdb_resource_status_is_correct(self):
        assert True


@pytest.mark.e2e_tls_x509_rs
class TestReplicaSetWithTLSRunning(KubernetesTester):
    def setup(self):
        for cert in self.yield_existing_csrs(get_rs_cert_names(mdb_resource, self.namespace, with_agent_certs=True)):
            self.approve_certificate(cert)
        KubernetesTester.wait_until('in_running_state', 120)

    @pytest.mark.xfail(reason="doesn't have good cert")
    def test_mdb_is_reachable_with_ssl(self):
        hosts = build_list_of_hosts(mdb_resource, self.namespace, 3)
        primary, secondaries = self.wait_for_rs_is_ready(hosts, ssl=True)

        assert primary is not None
        assert len(secondaries) == 2


@pytest.mark.e2e_tls_x509_rs
class TestsReplicaSetWithX509ClusterAuthentication(KubernetesTester):
    """
    update:
        patch: '[{"op":"add","path":"/spec/security","value": {"tls": {"enabled": true}, "clusterAuthenticationMode": "x509"}}]'
        file: test-tls-base-rs-require-ssl-upgrade.yaml
        wait_for_message: Not all internal cluster authentication certs have been approved by Kubernetes CA
    """

    def setup(self):
        for cert in self.yield_existing_csrs(get_rs_cert_names(mdb_resource, self.get_namespace(), with_internal_auth_certs=True)):
            self.approve_certificate(cert)
        KubernetesTester.in_running_state_failures_possible()

    def test_x509_enabled(self):
        mdb = self.get_resource()
        assert mdb["spec"]["security"]["clusterAuthenticationMode"] == "x509"

    @pytest.mark.xfail
    def test_mdb_is_reachable_with_no_ssl(self):
        hosts = build_list_of_hosts(mdb_resource, self.namespace, 3)
        primary, secondaries = self.wait_for_rs_is_ready(hosts, wait_for=20)

        assert primary is not None
        assert len(secondaries) == 2

    @pytest.mark.xfail(reason="doesn't have good cert")
    def test_mdb_is_reachable_with_ssl(self):
        hosts = build_list_of_hosts(mdb_resource, self.namespace, 3)
        primary, secondaries = self.wait_for_rs_is_ready(hosts, ssl=True)

        assert primary is not None
        assert len(secondaries) == 2


@pytest.mark.e2e_tls_x509_rs
class TestReplicaSetWithX509Remove(KubernetesTester):
    """
    name: Removal of X509 enabled Replica Set
    delete:
      file: test-tls-base-rs-require-ssl-upgrade.yaml
      wait_until: mongo_resource_deleted
      timeout: 240
    """

    def setup(self):
        # Deletes the certificate
        body = client.V1DeleteOptions()
        certs = get_rs_cert_names(mdb_resource, self.namespace, with_internal_auth_certs=True, with_agent_certs=True)
        for name in certs:
            self.certificates.delete_certificate_signing_request(name, body=body)

    def test_deletion(self):
        assert True


@pytest.mark.e2e_tls_x509_rs
class TestRemoveClusterAuthFromProject(KubernetesTester):
    @classmethod
    def setup_env(cls):
        cls.patch_config_map(cls.get_namespace(), "my-project", {"authenticationMode": "", "credentials": "my-credentials"})

    def test_noop(self):
        pass


@pytest.mark.e2e_tls_x509_rs
class TestReplicaSetWithoutTLSAgain(KubernetesTester):
    """
    create:
      file: test-tls-base-rs-require-ssl-upgrade.yaml
      wait_until: in_running_state
    """

    def test_mdb_is_reachable_with_no_ssl(self):
        hosts = build_list_of_hosts(mdb_resource, self.namespace, 3)
        primary, secondaries = self.wait_for_rs_is_ready(hosts, wait_for=120)

        assert primary is not None
        assert len(secondaries) == 2
