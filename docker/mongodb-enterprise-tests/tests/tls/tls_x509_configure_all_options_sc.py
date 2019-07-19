import pytest

from kubetester.kubetester import KubernetesTester
from kubetester.omtester import get_sc_cert_names, get_agent_cert_names

mdb_resource = "test-x509-all-options-sc"


@pytest.mark.e2e_tls_x509_configure_all_options_sc
class TestShardedClusterEnableX509(KubernetesTester):
    @classmethod
    def setup_env(cls):
        cls.patch_config_map(cls.get_namespace(), "my-project", {"authenticationMode": "x509", "credentials": "my-credentials"})

    def test_approve_certs(self):
        for cert in self.yield_existing_csrs(get_agent_cert_names(self.namespace,)):
            self.approve_certificate(cert)


@pytest.mark.e2e_tls_x509_configure_all_options_sc
class TestShardedClusterEnableAllOptions(KubernetesTester):
    """
    create:
      file: test-x509-all-options-sc.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
      timeout: 240
    """

    def test_approve_certs(self):
        for cert in self.yield_existing_csrs(get_sc_cert_names(mdb_resource, self.namespace, with_internal_auth_certs=True)):
            self.approve_certificate(cert)

    # TODO: use /mongodb-automation/server.pem but doesn't exist on test pod
    # def test_mdb_is_reachable_with_no_ssl(self):
    #     mongo_tester = ShardedClusterTester(mdb_resource, 2, ssl=True)
    #     mongo_tester.assert_connectivity()
