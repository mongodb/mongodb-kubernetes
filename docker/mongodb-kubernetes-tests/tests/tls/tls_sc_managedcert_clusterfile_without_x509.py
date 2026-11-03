"""A TLS sharded cluster with operator-managed certificates and SCRAM, where every component presents a
clusterfile to the others even though internal cluster authentication is not x509.

mongos, the config servers and the shards authenticate each other with the keyfile, but their
connections are mutual TLS, so each member still presents a client certificate. That is its
component's clusterfile, signed by the client CA, while the member certificates stay server-only. The
automation config sets net.tls.clusterFile and net.tls.clusterCAFile, but not clusterAuthMode.
"""

import pytest
from cryptography.x509.oid import ExtendedKeyUsageOID
from kubernetes.client.rest import ApiException
from kubetester import read_secret, try_load
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.tls.managedcert_helper import (
    assert_clusterfile_without_x509,
    extended_key_usages,
    managed_certificate_operator,
)

MDB_RESOURCE = "test-tls-sc-managedcert-clusterfile"
SHARD_COUNT = 1
MONGODS_PER_SHARD = 3
MONGOS_COUNT = 1
CONFIG_SERVER_COUNT = 3
COMPONENTS = [f"{MDB_RESOURCE}-mongos", f"{MDB_RESOURCE}-config"] + [f"{MDB_RESOURCE}-{i}" for i in range(SHARD_COUNT)]


@pytest.fixture(scope="module")
def operator(cert_manager: str, namespace: str) -> Operator:
    # cert_manager is a dependency, not used here: cert-manager has to be installed before the
    # operator starts, because the operator decides once, at startup, whether it may use cert-manager.
    return managed_certificate_operator(namespace)


@pytest.fixture(scope="module")
def sc(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("test-tls-sc-managedcert.yaml"), name=MDB_RESOURCE, namespace=namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["persistent"] = False
    resource["spec"]["shardCount"] = SHARD_COUNT
    resource["spec"]["mongodsPerShardCount"] = MONGODS_PER_SHARD
    resource["spec"]["mongosCount"] = MONGOS_COUNT
    resource["spec"]["configServerCount"] = CONFIG_SERVER_COUNT
    resource["spec"]["security"]["authentication"] = {"enabled": True, "modes": ["SCRAM"]}
    # No issuers, so the operator self-signs both the server CA and the client CA.
    resource["spec"]["security"]["managedCertificate"] = {}
    try_load(resource)
    return resource


@pytest.mark.e2e_sharded_cluster_tls_managedcert_clusterfile_without_x509
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@pytest.mark.e2e_sharded_cluster_tls_managedcert_clusterfile_without_x509
def test_sharded_cluster_running(sc: MongoDB):
    sc.update()
    sc.assert_reaches_phase(Phase.Running, timeout=1200)


@pytest.mark.e2e_sharded_cluster_tls_managedcert_clusterfile_without_x509
def test_every_component_has_a_server_only_member_cert_and_a_clusterfile(namespace: str):
    for component in COMPONENTS:
        member, clusterfile = f"{component}-managed-cert", f"{component}-managed-clusterfile"
        member_usages = extended_key_usages(namespace, member)
        assert ExtendedKeyUsageOID.SERVER_AUTH in member_usages, f"{member} does not allow serverAuth"
        assert ExtendedKeyUsageOID.CLIENT_AUTH not in member_usages, f"{member} should be server-only"
        assert ExtendedKeyUsageOID.CLIENT_AUTH in extended_key_usages(
            namespace, clusterfile
        ), f"{clusterfile} does not allow clientAuth"


@pytest.mark.e2e_sharded_cluster_tls_managedcert_clusterfile_without_x509
def test_no_agent_certificate(namespace: str):
    # The agent uses SCRAM, so it has no certificate.
    with pytest.raises(ApiException) as e:
        read_secret(namespace, f"{MDB_RESOURCE}-managed-agent-certs")
    assert e.value.status == 404


@pytest.mark.e2e_sharded_cluster_tls_managedcert_clusterfile_without_x509
def test_automation_config_sets_the_clusterfile_without_x509(sc: MongoDB):
    processes = MONGOS_COUNT + CONFIG_SERVER_COUNT + SHARD_COUNT * MONGODS_PER_SHARD
    assert_clusterfile_without_x509(sc.get_automation_config_tester().automation_config, MDB_RESOURCE, processes)
