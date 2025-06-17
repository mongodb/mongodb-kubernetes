import re

import pytest
from kubetester import try_load
from kubetester.certs import create_sharded_cluster_certs
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import is_multi_cluster, skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.shardedcluster.conftest import (
    enable_multi_cluster_deployment,
    get_member_cluster_clients_using_cluster_mapping,
    get_mongos_service_names,
)

MDB_RESOURCE_NAME = "test-tls-sc-additional-domains"


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    shard_distribution = None
    mongos_distribution = None
    config_srv_distribution = None
    if is_multi_cluster():
        shard_distribution = [1, 1, 1]
        mongos_distribution = [1, 1, None]
        config_srv_distribution = [1, 1, 1]

    create_sharded_cluster_certs(
        namespace,
        MDB_RESOURCE_NAME,
        shards=1,
        mongod_per_shard=1,
        config_servers=1,
        mongos=2,
        additional_domains=["additional-cert-test.com"],
        shard_distribution=shard_distribution,
        mongos_distribution=mongos_distribution,
        config_srv_distribution=config_srv_distribution,
    )


@pytest.fixture(scope="module")
def sc(namespace: str, server_certs: str, issuer_ca_configmap: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("test-tls-sc-additional-domains.yaml"), namespace=namespace)

    if try_load(resource):
        return resource

    resource.set_architecture_annotation()
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap

    if is_multi_cluster():
        enable_multi_cluster_deployment(
            resource=resource,
            shard_members_array=[1, 1, 1],
            mongos_members_array=[1, 1, None],
            configsrv_members_array=[1, 1, 1],
        )

    return resource.update()


@pytest.mark.e2e_tls_sc_additional_certs
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@pytest.mark.e2e_tls_sc_additional_certs
class TestShardedClusterWithAdditionalCertDomains:
    def test_sharded_cluster_running(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running, timeout=1200)

    @skip_if_local
    def test_has_right_certs(self, sc: MongoDB):
        """Check that mongos processes serving the right certificates."""
        for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
            cluster_idx = cluster_member_client.cluster_index
            for member_idx in range(sc.mongos_members_in_cluster(cluster_member_client.cluster_name)):
                mongos_pod_name = sc.mongos_pod_name(member_idx, cluster_idx)
                host = sc.mongos_hostname(member_idx, cluster_idx)
                assert any(
                    re.match(rf"{mongos_pod_name}\.additional-cert-test\.com", san)
                    for san in KubernetesTester.get_mongo_server_sans(host)
                )

    @skip_if_local
    def test_can_still_connect(self, sc: MongoDB, ca_path: str):
        service_names = get_mongos_service_names(sc)
        tester = sc.tester(ca_path=ca_path, use_ssl=True, service_names=service_names)
        tester.assert_connectivity()


@pytest.mark.e2e_tls_sc_additional_certs
def test_remove_additional_certificate_domains(sc: MongoDB):
    sc["spec"]["security"]["tls"].pop("additionalCertificateDomains")
    sc.update()
    sc.assert_reaches_phase(Phase.Running, timeout=240)


@pytest.mark.e2e_tls_sc_additional_certs
@skip_if_local
def test_can_still_connect(sc: MongoDB, ca_path: str):
    service_names = get_mongos_service_names(sc)
    tester = sc.tester(ca_path=ca_path, use_ssl=True, service_names=service_names)
    tester.assert_connectivity()
