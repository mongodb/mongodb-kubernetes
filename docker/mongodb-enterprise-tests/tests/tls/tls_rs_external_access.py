import base64
import re

import pytest

from kubetester.omtester import get_rs_cert_names
from kubetester.kubetester import KubernetesTester, skip_if_local
from kubetester.mongotester import ReplicaSetTester


@pytest.mark.e2e_tls_rs_external_access
class TestReplicaSetWithExternalAccess(KubernetesTester):
    """
    create:
      file: test-tls-base-rs-external-access.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
      timeout: 240
    """

    def test_horizon_certs_generated(self):
        expected_horizon_names = {
            "mdb0-test-website.com",
            "mdb1-test-website.com",
            "mdb2-test-website.com",
        }
        csr_names = get_rs_cert_names(
            "test-tls-base-rs-external-access", self.namespace
        )
        for csr_name in self.yield_existing_csrs(csr_names):
            horizon_name = [
                s for s in self.get_csr_sans(csr_name) if s in expected_horizon_names
            ][0]
            expected_horizon_names.remove(horizon_name)

            self.approve_certificate(csr_name)
        KubernetesTester.wait_until("in_running_state")

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
        assert any(
            san.endswith("test-website.com") for san in self.get_mongo_server_sans(host)
        )


@pytest.mark.e2e_tls_rs_external_access
class TestReplicaSetScaleWithExternalAccess(KubernetesTester):
    init = {
        "update": {
            "file": "test-tls-base-rs-external-access.yaml",
            "wait_for_message": "Not all certificates have been approved by Kubernetes CA",
            "timeout": 240,
            "patch": [
                {
                    "op": "add",
                    "path": "/spec/connectivity/replicaSetHorizons/-",
                    "value": {"test-horizon": "mdb3-test-website.com:1337"},
                },
                {"op": "replace", "path": "/spec/members", "value": 4},
            ],
        }
    }

    def test_certs_approved(self):
        csr_names = get_rs_cert_names(
            "test-tls-base-rs-external-access", self.namespace, members=4
        )
        for csr_name in self.yield_existing_csrs(csr_names):
            self.approve_certificate(csr_name)
        KubernetesTester.wait_until("in_running_state")


@pytest.mark.e2e_tls_rs_external_access
class TestReplicaSetExternalAccessInvalidCerts(KubernetesTester):
    init = {
        "update": {
            "file": "test-tls-base-rs-external-access.yaml",
            "wait_for_message": re.compile(
                r"Certificate request for .+ doesn't have all required domains. Please manually remove the CSR in order to proceed."
            ),
            "patch": [
                {
                    "op": "replace",
                    "path": "/spec/connectivity/replicaSetHorizons/0/test-horizon",
                    "value": "mdb5-test-website.com:1337",
                }
            ],
            "timeout": 10,
        }
    }

    def test_horizon_certs_generated(self):
        return True


@pytest.mark.e2e_tls_rs_external_access
class TestReplicaSetCanRemoveExternalAccess(KubernetesTester):
    """
    update:
      file: test-tls-base-rs-external-access.yaml
      wait_until: in_running_state
      patch: '[{"op":"replace","path":"/spec/connectivity/replicaSetHorizons", "value": []}]'
      timeout: 240
    """

    def test_can_remove_horizons(self):
        return True


@pytest.mark.e2e_tls_rs_external_access
class TestReplicaSetWithNoTLSDeletion(KubernetesTester):
    """
    delete:
      file: test-tls-base-rs-external-access.yaml
      wait_until: mongo_resource_deleted_no_om
      timeout: 240
    """

    def test_deletion(self):
        assert True


@pytest.mark.e2e_tls_rs_external_access
class TestReplicaSetWithMultipleHorizons(KubernetesTester):
    """
    create:
      file: test-tls-rs-external-access-multiple-horizons.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
      timeout: 240
    """

    def test_horizon_certs_generated(self):
        expected_horizon_names = {
            ("mdb0-test-1-website.com", "mdb0-test-2-website.com"),
            ("mdb1-test-1-website.com", "mdb1-test-2-website.com"),
            ("mdb2-test-1-website.com", "mdb2-test-2-website.com"),
        }
        resource_name = "test-tls-rs-external-access-multiple-horizons"
        csr_names = get_rs_cert_names(resource_name, self.namespace)
        for csr_name in self.yield_existing_csrs(csr_names):
            # ensure that the CSR has one of expected sets of Subject
            # Alternative Names
            sans = self.get_csr_sans(csr_name)
            assert any(
                all(name in sans for name in expected)
                for expected in expected_horizon_names
            )

            print("Approving certificate {}".format(csr_name))
            self.approve_certificate(csr_name)
        KubernetesTester.wait_until("in_running_state")

    def test_automation_config_is_right(self):
        ac = self.get_automation_config()
        members = ac["replicaSets"][0]["members"]
        horizons = [m["horizons"] for m in members]
        assert horizons == [
            {
                "test-horizon-1": "mdb0-test-1-website.com:1337",
                "test-horizon-2": "mdb0-test-2-website.com:2337",
            },
            {
                "test-horizon-1": "mdb1-test-1-website.com:1338",
                "test-horizon-2": "mdb1-test-2-website.com:2338",
            },
            {
                "test-horizon-1": "mdb2-test-1-website.com:1339",
                "test-horizon-2": "mdb2-test-2-website.com:2339",
            },
        ]


@pytest.mark.e2e_tls_rs_external_access
class TestReplicaSetDeleteMultipleHorizon(KubernetesTester):
    """
    delete:
      file: test-tls-rs-external-access-multiple-horizons.yaml
      wait_until: mongo_resource_deleted_no_om
      timeout: 240
    """

    def test_deletion(self):
        assert True
