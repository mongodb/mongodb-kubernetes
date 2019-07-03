import pytest
from kubernetes import client
from kubetester.kubetester import KubernetesTester, build_list_of_hosts
from kubetester.mongotester import ReplicaSetTester
from kubetester.omtester import get_agent_cert_names, get_rs_cert_names

mdb_resource = "test-x509-all-options-rs"


@pytest.mark.e2e_tls_x509_configure_all_options_rs
class TestsReplicaSetWithNoTLSWithX509Project(KubernetesTester):
    @classmethod
    def setup_env(cls):
        cls.patch_config_map(cls.get_namespace(), "my-project", {"authenticationMode": "x509", "credentials": "my-credentials"})

    def test_approve_certs(self):
        for cert in self.yield_existing_csrs(get_agent_cert_names(self.namespace,)):
            self.approve_certificate(cert)


@pytest.mark.e2e_tls_x509_configure_all_options_rs
class TestReplicaSetEnableAllOptions(KubernetesTester):
    """
    create:
      file: test-x509-all-options-rs.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
      timeout: 240
    """

    def test_approve_certs(self):
        for cert in self.yield_existing_csrs(get_rs_cert_names(mdb_resource, self.namespace, with_internal_auth_certs=True)):
            self.approve_certificate(cert)

        KubernetesTester.wait_until(KubernetesTester.in_running_state)

    # TODO: use /mongodb-automation/server.pem but doesn't exist on test pod
    # def test_mdb_is_reachable(self):
    #     mongo_tester = ReplicaSetTester(mdb_resource, 3, ssl=True)
    #     mongo_tester.assert_connectivity()


