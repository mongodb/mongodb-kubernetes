import re

import pytest

from kubetester.omtester import get_sc_cert_names
from kubetester.kubetester import KubernetesTester, skip_if_local
from kubetester.mongotester import ShardedClusterTester

MDB_RESOURCE_NAME = "test-tls-sc-additional-domains"


@pytest.mark.e2e_tls_sc_additional_certs_transition
class TestShardedClusterWithoutAdditionalCertDomains(KubernetesTester):
    init = {
        "create": {
            "file": "test-tls-sc-additional-domains.yaml",
            "wait_for_message": "Not all certificates have been approved by Kubernetes CA",
            "timeout": 240,
            "patch": [{"op": "remove", "path": "/spec/security/tls/additionalCertificateDomains"}],
        },
    }

    def test_certs_generated(self):
        csr_names = get_sc_cert_names(
            MDB_RESOURCE_NAME,
            self.namespace,
            num_shards=1,
            num_mongos=2,
            config_members=1,
            members=1
        )
        for csr_name in self.yield_existing_csrs(csr_names):
            self.approve_certificate(csr_name)
        self.wait_until('in_running_state')

    @skip_if_local
    def test_has_right_certs(self):
        """Check that only the correct SANs are present."""
        for i in range(2):
            host = f"{MDB_RESOURCE_NAME}-mongos-{i}.{MDB_RESOURCE_NAME}-svc.{self.namespace}.svc"
            assert set(self.get_mongo_server_sans(host)) == {
                f"{MDB_RESOURCE_NAME}-mongos-{i}",
                f"{host}.cluster.local",
            }


@pytest.mark.e2e_tls_sc_additional_certs_transition
class TestShardedClustertWithAdditionalCertDomains(KubernetesTester):
    """
    Check that when additionalCertificateDomains are added the resource fails
    to reconcile and requires the existing CSRs to be removed.
    """
    init = {
        "update": {
            "file": "test-tls-sc-additional-domains.yaml",
            "wait_for_message": "Please manually remove the CSR in order to proceed.",
            "timeout": 240,
        },
    }

    def test_certs_approved(self):
        csr_names = get_sc_cert_names(
            MDB_RESOURCE_NAME,
            self.namespace,
            num_shards=1,
            num_mongos=2,
            config_members=1,
            members=1
        )
        for csr_name in self.yield_existing_csrs(csr_names):
            self.delete_csr(csr_name)
        self.wait_for_status_message({
            "wait_for_message": "Not all certificates have been approved",
            "timeout": 60
        })
        for csr_name in self.yield_existing_csrs(csr_names):
            self.approve_certificate(csr_name)
        KubernetesTester.wait_until('in_running_state')

    @skip_if_local
    def test_has_right_certs(self):
        """Check that mongos processes are serving the right certificates."""
        for i in range(2):
            host = f"{MDB_RESOURCE_NAME}-mongos-{i}.{MDB_RESOURCE_NAME}-svc.{self.namespace}.svc"
            assert any(
                re.match(fr"{MDB_RESOURCE_NAME}-mongos-{i}\.additional-cert-test\.com", san)
                for san
                in self.get_mongo_server_sans(host)
            )

    @skip_if_local
    def test_can_still_connect(self):
        tester = ShardedClusterTester(MDB_RESOURCE_NAME, ssl=True, mongos_count=2)
        tester.assert_connectivity()
