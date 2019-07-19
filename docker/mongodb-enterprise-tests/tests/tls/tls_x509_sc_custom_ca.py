import pytest

from kubernetes import client
from kubetester.kubetester import KubernetesTester, skip_if_local
from kubetester.mongotester import ShardedClusterTester
from kubetester.omtester import get_agent_cert_names, get_sc_cert_names

from typing import Dict, List

mdb_resource = "test-tls-base-sc-require-ssl"


def host_groups() -> Dict[str, List[str]]:
    "Returns the list of generated certs we use with this deployment"
    shard0 = ["{}-0-{}".format(mdb_resource, i) for i in range(3)]
    config = ["{}-config-{}".format(mdb_resource, i) for i in range(3)]
    mongos = ["{}-mongos-{}".format(mdb_resource, i) for i in range(2)]
    return {
        f"{mdb_resource}-0": shard0,
        f"{mdb_resource}-config": config,
        f"{mdb_resource}-mongos": mongos
    }


@pytest.mark.e2e_tls_x509_sc_custom_ca
class TestShardedClusterWithTLSWithX509Project(KubernetesTester):
    def test_enable_x509(self):
        self.patch_config_map(self.get_namespace(), "my-project", {"authenticationMode": "x509", "credentials": "my-credentials"})
        for cert in self.yield_existing_csrs(get_agent_cert_names(self.get_namespace())):
            self.approve_certificate(cert)


@pytest.mark.e2e_tls_x509_sc_custom_ca
class TestClusterWithTLSCreation(KubernetesTester):
    """
    name: Sharded Cluster With TLS Creation
    description: |
      Creates a MongoDB object with the ssl attribute on. The MongoDB object will go to Failed
      state because of missing certificates.
    create:
      file: test-tls-base-sc-require-ssl.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
    """

    def test_mdb_resource_status_is_correct(self):
        assert True


@pytest.mark.e2e_tls_x509_sc_custom_ca
class TestShardedClusterWithTLSRunning(KubernetesTester):
    def test_approve_certificates(self):
        for cert in self.yield_existing_csrs(get_sc_cert_names(mdb_resource, self.get_namespace())):
            self.approve_certificate(cert)

        KubernetesTester.wait_until('in_running_state')


@pytest.mark.e2e_tls_x509_sc_custom_ca
class TestShardedClusterWithX509ClusterAuthentication(KubernetesTester):
    """
    update:
        patch: '[{"op":"replace","path":"/spec/security","value": {"tls": {"enabled": true}, "clusterAuthenticationMode": "x509"}}]'
        file: test-tls-base-sc-require-ssl.yaml
        wait_for_message: Not all internal cluster authentication certs have been approved by Kubernetes CA
    """

    def test_running_state_once_internal_cluster_auth_certs_approved(self):
        cert_names = get_sc_cert_names(mdb_resource, self.get_namespace(), with_internal_auth_certs=True)
        for cert in self.yield_existing_csrs(cert_names):
            self.approve_certificate(cert)
        KubernetesTester.wait_until('in_running_state')

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
      file: test-tls-base-sc-require-ssl.yaml
      wait_until: mongo_resource_deleted
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
      file: test-tls-base-sc-require-ssl-custom-ca.yaml
      patch: '[{"op":"add", "path":"/spec/security/clusterAuthenticationMode", "value": "x509"}]'
      wait_until: in_running_state
    """

    def test_mdb_should_reach_goal_state(self):
        mdb = self.get_resource()
        assert mdb["spec"]["security"]["clusterAuthenticationMode"] == "x509"
