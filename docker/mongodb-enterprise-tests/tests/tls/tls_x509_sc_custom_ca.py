import pytest

from kubernetes import client
from kubetester.kubetester import KubernetesTester
from kubetester.omtester import get_sc_cert_names

from typing import Dict, List

MDB_RESOURCE = "test-x509-sc-custom-ca"


def host_groups() -> Dict[str, List[str]]:
    "Returns the list of generated certs we use with this deployment"
    shard0 = ["{}-0-{}".format(MDB_RESOURCE, i) for i in range(3)]
    config = ["{}-config-{}".format(MDB_RESOURCE, i) for i in range(3)]
    mongos = ["{}-mongos-{}".format(MDB_RESOURCE, i) for i in range(2)]
    return {
        f"{MDB_RESOURCE}-0": shard0,
        f"{MDB_RESOURCE}-config": config,
        f"{MDB_RESOURCE}-mongos": mongos,
    }


@pytest.mark.e2e_tls_x509_sc_custom_ca
class TestClusterWithTLSCreation(KubernetesTester):
    """
    name: Sharded Cluster With TLS Creation
    description: |
      Creates a MongoDB object with the ssl attribute on. The MongoDB object will go to Failed
      state because of missing certificates.
    create:
      file: test-x509-sc-custom-ca.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
    """

    def test_approve_certificates(self):
        for cert in self.yield_existing_csrs(
            get_sc_cert_names(
                MDB_RESOURCE,
                self.get_namespace(),
                with_internal_auth_certs=True,
                with_agent_certs=True,
            )
        ):
            self.approve_certificate(cert)

        KubernetesTester.wait_until("in_running_state")

    def test_x509_enabled(self):
        mdb = self.get_resource()
        assert mdb["spec"]["security"]["clusterAuthenticationMode"] == "x509"


@pytest.mark.e2e_tls_x509_sc_custom_ca
class TestShardedClusterIsRemoved(KubernetesTester):
    """
    name: Removal of x509 Sharded Cluster
    description: |
      There will be no more CSR in the cluster after this, the certificates that will
      be used are the ones already stored in the Secrets.
    delete:
      file: test-x509-sc-custom-ca.yaml
      wait_until: mongo_resource_deleted_no_om
    """

    def test_remove_certificates(self):
        body = client.V1DeleteOptions()
        for _, hosts in host_groups().items():
            for host in hosts:
                self.certificates.delete_certificate_signing_request(
                    "{}.{}".format(host, self.namespace), body=body
                )


@pytest.mark.e2e_tls_x509_sc_custom_ca
class TestClusterWithX509CreateCAMap(KubernetesTester):
    @classmethod
    def setup_env(cls):
        data = cls.read_configmap("default", "ca-certificates")
        cls.create_configmap(cls.get_namespace(), "customer-ca", data)

    def test_true(self):
        assert True


@pytest.mark.e2e_tls_x509_sc_custom_ca
class TestClusterWithX509WithExistingCerts(KubernetesTester):
    """
    name: Sharded Cluster With TLS Creation
    description: |
      Creates a MongoDB object exactly like the one we just removed. The only difference is that
      we are setting the security.tls.ca parameter to point to a "custom" CA, even if it is the
      ones that the operator created.
    create:
      file: test-x509-sc-custom-ca.yaml
      wait_until: in_running_state
    """

    def test_mdb_should_reach_goal_state(self):
        mdb = self.get_resource()
        assert mdb["spec"]["security"]["clusterAuthenticationMode"] == "x509"
