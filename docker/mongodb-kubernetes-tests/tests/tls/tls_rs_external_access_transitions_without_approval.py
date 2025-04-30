import pytest
from kubetester.kubetester import KubernetesTester, skip_if_local
from kubetester.mongotester import ReplicaSetTester
from kubetester.omtester import get_rs_cert_names
from kubetester.operator import Operator


@pytest.mark.e2e_tls_rs_external_access_tls_transition_without_approval
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@pytest.mark.e2e_tls_rs_external_access_tls_transition_without_approval
class TestReplicaSetWithExternalAccess(KubernetesTester):
    init = {
        "create": {
            "file": "test-tls-base-rs-external-access.yaml",
            "wait_for_message": "Not all certificates have been approved by Kubernetes CA",
            "timeout": 60,
            "patch": [{"op": "remove", "path": "/spec/connectivity/replicaSetHorizons"}],
        }
    }

    def test_tls_certs_approved(self):
        pass


@pytest.mark.e2e_tls_rs_external_access_tls_transition_without_approval
class TestReplicaSetExternalAccessAddHorizons(KubernetesTester):
    init = {
        "update": {
            "file": "test-tls-base-rs-external-access.yaml",
            "wait_for_message": "Please manually remove the CSR in order to proceed.",
            "timeout": 60,
        }
    }

    def test_certs_approved(self):
        csr_names = get_rs_cert_names("test-tls-base-rs-external-access", self.namespace)
        for csr_name in self.yield_existing_csrs(csr_names):
            self.delete_csr(csr_name)
        self.wait_for_status_message(
            {
                "wait_for_message": "Not all certificates have been approved",
                "timeout": 30,
            }
        )
        for csr_name in self.yield_existing_csrs(csr_names):
            self.approve_certificate(csr_name)
        KubernetesTester.wait_until("in_running_state", 360)

    @skip_if_local
    def test_can_still_connect(self):
        tester = ReplicaSetTester("test-tls-base-rs-external-access", 3, ssl=True)
        tester.assert_connectivity()

    def test_automation_config_is_right(self):
        ac = self.get_automation_config()
        members = ac["replicaSets"][0]["members"]
        horizon_names = [m["horizons"] for m in members]
        assert horizon_names == [
            {"test-horizon": "mdb0-test-website.com:1337"},
            {"test-horizon": "mdb1-test-website.com:1337"},
            {"test-horizon": "mdb2-test-website.com:1337"},
        ]

    @skip_if_local
    def test_has_right_certs(self):
        """
        Check that mongod processes behind the replica set service are
        serving the right certificates.
        """
        host = f"test-tls-base-rs-external-access-svc.{self.namespace}.svc"
        assert any(san.endswith("test-website.com") for san in self.get_mongo_server_sans(host))


@pytest.mark.e2e_tls_rs_external_access_tls_transition_without_approval
class TestExternalAccessDeletion(KubernetesTester):
    """
    delete:
      file: test-tls-base-rs-external-access.yaml
      wait_until: mongo_resource_deleted_no_om
      timeout: 240
    """

    def test_deletion(self):
        assert True
