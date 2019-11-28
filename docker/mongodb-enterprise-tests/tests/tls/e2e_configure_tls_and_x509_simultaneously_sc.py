import pytest

from kubetester.kubetester import KubernetesTester
from kubetester.mongotester import ShardedClusterTester
from kubetester.omtester import get_sc_cert_names

MDB_RESOURCE = "sh001-base"


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_sc
class TestCreateShardedCluster(KubernetesTester):
    """
    create:
      file: sharded-cluster.yaml
      wait_until: in_running_state
    """

    def test_connectivity(self):
        tester = ShardedClusterTester(MDB_RESOURCE, 2)
        tester.assert_connectivity()


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_sc
class TestEnableX509AndTls(KubernetesTester):
    """
    update:
      file: sharded-cluster.yaml
      patch: '[{"op":"add","path":"/spec/security", "value" : {"authentication" : {"enabled" : true, "modes" : ["X509"]}, "tls" : { "enabled" : true }}} ]'
      wait_for_message: Not all certificates have been approved by Kubernetes CA
    """

    def test_approve_certificates(self):
        for cert in self.yield_existing_csrs(
            get_sc_cert_names(MDB_RESOURCE, self.namespace, with_agent_certs=True)
        ):
            self.approve_certificate(cert)
        KubernetesTester.wait_until(KubernetesTester.in_running_state)


@pytest.mark.e2e_configure_tls_and_x509_simultaneously_sc
class TestShardedClusterDeleted(KubernetesTester):
    """
    description: |
      Deletes the Sharded Cluster
    delete:
      file: sharded-cluster.yaml
      wait_until: mongo_resource_deleted
      timeout: 240
    """

    def test_noop(self):
        pass
