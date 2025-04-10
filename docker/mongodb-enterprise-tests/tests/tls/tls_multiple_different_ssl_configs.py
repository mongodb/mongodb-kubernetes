import pytest
from kubetester.kubetester import KubernetesTester, skip_if_local
from kubetester.mongotester import ReplicaSetTester
from kubetester.operator import Operator

mdb_resources = {
    "ssl_enabled": "test-tls-base-rs-require-ssl",
    "ssl_disabled": "test-no-tls-no-status",
}


def cert_names(namespace, members=3):
    return ["{}-{}.{}".format(mdb_resources["ssl_enabled"], i, namespace) for i in range(members)]


@pytest.mark.e2e_tls_multiple_different_ssl_configs
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@pytest.mark.e2e_tls_multiple_different_ssl_configs
class TestMultipleCreation(KubernetesTester):
    """
    name: 2 Replica Sets on same project with different SSL configurations
    description: |
      2 MDB/ReplicaSet object will be created, one of them will be have TLS enabled (required)
      and the other one will not. Both of them should work, and listen on respective SSL/non SSL endpoints.
    create:
    - file: test-tls-base-rs-require-ssl.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
      timeout: 120

    - file: test-no-tls-no-status.yaml
      wait_until: in_running_state
      timeout: 120
    """

    def test_mdb_tls_resource_status_is_correct(self):
        mdb = self.customv1.get_namespaced_custom_object(
            "mongodb.com", "v1", self.namespace, "mongodb", mdb_resources["ssl_enabled"]
        )
        assert mdb["status"]["message"] == "Not all certificates have been approved by Kubernetes CA"

        mdb = self.customv1.get_namespaced_custom_object(
            "mongodb.com",
            "v1",
            self.namespace,
            "mongodb",
            mdb_resources["ssl_disabled"],
        )
        assert mdb["status"]["phase"] == "Running"


@pytest.mark.e2e_tls_multiple_different_ssl_configs
class TestMultipleApproval(KubernetesTester):
    """
    name: Approval of certificates
    description: |
      Approves the certificates in Kubernetes, the MongoDB resource should move to Successful state.
    """

    def setup_method(self):
        super().setup_method()
        [self.approve_certificate(cert) for cert in cert_names(self.namespace)]

    def test_noop(self):
        assert True


@pytest.mark.e2e_tls_multiple_different_ssl_configs
class TestMultipleRunning0(KubernetesTester):
    """
    name: Both MDB objects should be in running state.
    wait:
    - resource: test-tls-base-rs-require-ssl
      until: in_running_state
      timeout: 200
    - resource: test-no-tls-no-status
      until: in_running_state
      timeout: 60
    """

    @skip_if_local()
    def test_mdb_ssl_enabled_is_not_reachable_with_no_ssl(self):
        mongo_tester = ReplicaSetTester(mdb_resources["ssl_enabled"], 3)
        mongo_tester.assert_no_connection()

    @skip_if_local()
    def test_mdb_ssl_enabled_is_reachable_with_ssl(self):
        mongo_tester = ReplicaSetTester(mdb_resources["ssl_enabled"], 3, ssl=True)
        mongo_tester.assert_connectivity()

    @skip_if_local()
    def test_mdb_ssl_disabled_is_reachable_with_no_ssl(self):
        mongo_tester = ReplicaSetTester(mdb_resources["ssl_disabled"], 3)
        mongo_tester.assert_connectivity()

    @skip_if_local()
    def test_mdb_ssl_disabled_is_not_reachable_with_ssl(self):
        mongo_tester = ReplicaSetTester(mdb_resources["ssl_disabled"], 3, ssl=True)
        mongo_tester.assert_no_connection()
