import pytest
from kubetester import try_load
from kubetester.certs import Certificate, create_sharded_cluster_certs
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import is_multi_cluster, skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.mongotester import ShardedClusterTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.shardedcluster.conftest import (
    enable_multi_cluster_deployment,
    get_mongos_service_names,
)

MDB_RESOURCE_NAME = "test-tls-base-sc-require-ssl"


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
        mongod_per_shard=3,
        config_servers=3,
        mongos=2,
        shard_distribution=shard_distribution,
        mongos_distribution=mongos_distribution,
        config_srv_distribution=config_srv_distribution,
    )


@pytest.fixture(scope="module")
def sc(namespace: str, server_certs: str, issuer_ca_configmap: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("test-tls-base-sc-require-ssl-custom-ca.yaml"), namespace=namespace)

    if try_load(resource):
        return resource

    resource.set_architecture_annotation()
    resource["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap

    if is_multi_cluster():
        enable_multi_cluster_deployment(
            resource=resource,
            shard_members_array=[1, 1, 1],
            mongos_members_array=[1, 1, None],
            configsrv_members_array=[1, 1, 1],
        )

    return resource.update()


@pytest.mark.e2e_sharded_cluster_tls_require_custom_ca
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@pytest.mark.e2e_sharded_cluster_tls_require_custom_ca
class TestClusterWithTLSCreation:
    def test_sharded_cluster_running(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running, timeout=1200)

    @skip_if_local
    def test_mongos_are_reachable_with_ssl(self, sc: MongoDB, ca_path: str):
        service_names = get_mongos_service_names(sc)
        tester = sc.tester(ca_path=ca_path, use_ssl=True, service_names=service_names)
        tester.assert_connectivity()

    @skip_if_local
    def test_mongos_are_not_reachable_with_no_ssl(self, sc: MongoDB):
        service_names = get_mongos_service_names(sc)
        tester = sc.tester(use_ssl=False, service_names=service_names)
        tester.assert_no_connection()


@pytest.mark.e2e_sharded_cluster_tls_require_custom_ca
class TestCertificateIsRenewed:
    def test_mdb_reconciles_succesfully(self, sc: MongoDB, namespace: str):
        cert = Certificate(name=f"{MDB_RESOURCE_NAME}-0-cert", namespace=namespace).load()
        cert["spec"]["dnsNames"].append("foo")
        cert.update()
        sc.assert_reaches_phase(Phase.Running, timeout=1200)

    @skip_if_local
    def test_mongos_are_reachable_with_ssl(self, sc: MongoDB, ca_path: str):
        service_names = get_mongos_service_names(sc)
        tester = sc.tester(use_ssl=True, ca_path=ca_path, service_names=service_names)
        tester.assert_connectivity()

    @skip_if_local
    def test_mongos_are_not_reachable_with_no_ssl(
        self,
        sc: MongoDB,
    ):
        service_names = get_mongos_service_names(sc)
        tester = sc.tester(use_ssl=False, service_names=service_names)
        tester.assert_no_connection()
