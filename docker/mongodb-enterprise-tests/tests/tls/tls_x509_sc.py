import pytest
from kubetester.kubetester import KubernetesTester

mdb_resource = "test-tls-base-sc-require-ssl"


def get_agent_cert_names(namespace):
    agent_names = ['mms-automation-agent', 'mms-backup-agent', 'mms-monitoring-agent']
    return ['{}.{}'.format(agent_name, namespace) for agent_name in agent_names]


def get_cert_names(
    namespace,
    *,
    num_shards=1,
    members=3,
    config_members=3,
    num_mongos=2,
    with_internal_auth_certs=False,
):
    names = []

    for shard_num in range(num_shards):
        for member in range(members):
            # e.g. test-tls-x509-sc-0-1.developer14
            names.append('{}-{}-{}.{}'.format(mdb_resource, shard_num, member, namespace))
            if with_internal_auth_certs:
                # e.g. test-tls-x509-sc-0-2-clusterfile.developer14
                names.append('{}-{}-{}-clusterfile.{}'.format(mdb_resource, shard_num, member, namespace))

    for member in range(config_members):
        # e.g. test-tls-x509-sc-config-1.developer14
        names.append('{}-config-{}.{}'.format(mdb_resource, member, namespace))
        if with_internal_auth_certs:
            # e.g. test-tls-x509-sc-config-1-clusterfile.developer14
            names.append('{}-config-{}-clusterfile.{}'.format(mdb_resource, member, namespace))

    for mongos in range(num_mongos):
        # e.g.test-tls-x509-sc-mongos-1.developer14
        names.append('{}-mongos-{}.{}'.format(mdb_resource, mongos, namespace))
        if with_internal_auth_certs:
            # e.g. test-tls-x509-sc-mongos-0-clusterfile.developer14
            names.append('{}-mongos-{}-clusterfile.{}'.format(mdb_resource, mongos, namespace))

    return names


@pytest.mark.e2e_tls_x509_sc
class TestShardedClusterWithTLSWithX509Project(KubernetesTester):
    def test_enable_x509(self):
        self.patch_config_map(self.get_namespace(), "my-project", {"authenticationMode": "x509", "credentials": "my-credentials"})
        for cert in self.yield_existing_csrs(get_agent_cert_names(self.get_namespace())):
            self.approve_certificate(cert)


@pytest.mark.e2e_tls_x509_sc
class TestClusterWithTLSCreation(KubernetesTester):
    """
    name: Sharded Cluster With TLS Creation
    description: |
      Creates a MongoDB object with the ssl attribute on. The MongoDB object will go to Failed
      state because of missing certificates.
    create:
      file: test-tls-base-sc-require-ssl.yaml
      wait_for_message: Not all certificates have been approved by Kubernetes CA
      timeout: 120
    """

    def test_mdb_resource_status_is_correct(self):
        assert True

@pytest.mark.e2e_tls_x509_sc
class TestShardedClusterWithTLSRunning(KubernetesTester):
    def test_approve_certificates(self):
        for cert in self.yield_existing_csrs(get_cert_names(self.get_namespace())):
            self.approve_certificate(cert)
        KubernetesTester.wait_until('in_running_state', 360)


@pytest.mark.e2e_tls_x509_sc
class TestsShardedClusterWithX509ClusterAuthentication(KubernetesTester):
    """
    update:
        patch: '[{"op":"replace","path":"/spec/security","value": {"tls": {"enabled": true}, "clusterAuthenticationMode": "x509"}}]'
        file: test-tls-base-sc-require-ssl.yaml
        wait_for_message: Not all internal cluster authentication certs have been approved by Kubernetes CA
    """

    def test_running_state_once_internal_cluster_auth_certs_approved(self):
        cert_names = get_cert_names(self.get_namespace(), with_internal_auth_certs=True)
        for cert in self.yield_existing_csrs(cert_names):
            self.approve_certificate(cert)
        KubernetesTester.wait_until('in_running_state', 0)

    def test_x509_enabled(self):
        mdb = self.get_resource()
        assert mdb["spec"]["security"]["clusterAuthenticationMode"] == "x509"
