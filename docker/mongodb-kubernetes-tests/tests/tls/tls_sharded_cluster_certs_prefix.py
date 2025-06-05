import kubernetes
from kubetester import try_load
from kubetester.certs import Certificate, create_sharded_cluster_certs
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import is_multi_cluster, skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import central_cluster_client
from tests.shardedcluster.conftest import (
    enable_multi_cluster_deployment,
    get_mongos_service_names,
)

MDB_RESOURCE = "sharded-cluster-custom-certs"


@fixture(scope="module")
def all_certs(central_cluster_client: kubernetes.client.ApiClient, issuer: str, namespace: str) -> None:
    """Generates all required TLS certificates: Servers and Client/Member."""

    shard_distribution = None
    mongos_distribution = None
    config_srv_distribution = None
    if is_multi_cluster():
        shard_distribution = [1, 1, 1]
        mongos_distribution = [1, 1, None]
        config_srv_distribution = [1, 1, 1]

    create_sharded_cluster_certs(
        namespace,
        MDB_RESOURCE,
        shards=1,
        mongod_per_shard=3,
        config_servers=3,
        mongos=2,
        x509_certs=True,
        secret_prefix="prefix-",
        shard_distribution=shard_distribution,
        mongos_distribution=mongos_distribution,
        config_srv_distribution=config_srv_distribution,
    )


@fixture(scope="module")
def sc(namespace: str, issuer_ca_configmap: str, custom_mdb_version: str, all_certs) -> MongoDB:
    resource = MongoDB.from_yaml(
        load_fixture("test-tls-base-sc-require-ssl.yaml"),
        name=MDB_RESOURCE,
        namespace=namespace,
    )

    if try_load(resource):
        return resource

    resource["spec"]["security"] = {
        "tls": {
            "enabled": True,
            "ca": issuer_ca_configmap,
        },
        "certsSecretPrefix": "prefix",
    }

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()

    if is_multi_cluster():
        enable_multi_cluster_deployment(
            resource=resource,
            shard_members_array=[1, 1, 1],
            mongos_members_array=[1, 1, None],
            configsrv_members_array=[1, 1, 1],
        )

    return resource.update()


@mark.e2e_tls_sharded_cluster_certs_prefix
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_tls_sharded_cluster_certs_prefix
def test_sharded_cluster_with_prefix_gets_to_running_state(sc: MongoDB):
    sc.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_tls_sharded_cluster_certs_prefix
@skip_if_local
def test_sharded_cluster_has_connectivity_with_tls(sc: MongoDB, ca_path: str):
    service_names = get_mongos_service_names(sc)
    tester = sc.tester(ca_path=ca_path, use_ssl=True, service_names=service_names)
    tester.assert_connectivity()


@mark.e2e_tls_sharded_cluster_certs_prefix
@skip_if_local
def test_sharded_cluster_has_no_connectivity_without_tls(sc: MongoDB):
    service_names = get_mongos_service_names(sc)
    tester = sc.tester(use_ssl=False, service_names=service_names)
    tester.assert_no_connection()


@mark.e2e_tls_sharded_cluster_certs_prefix
def test_rotate_tls_certificate(sc: MongoDB, namespace: str):
    # update the shard cert
    cert = Certificate(name=f"prefix-{MDB_RESOURCE}-0-cert", namespace=namespace).load()
    cert["spec"]["dnsNames"].append("foo")
    cert.update()

    sc.assert_abandons_phase(Phase.Running)
    sc.assert_reaches_phase(Phase.Running, timeout=800)


@mark.e2e_tls_sharded_cluster_certs_prefix
def test_disable_tls(sc: MongoDB):
    last_transition = sc.get_status_last_transition_time()
    sc.load()
    sc["spec"]["security"]["tls"]["enabled"] = False
    sc.update()

    sc.assert_state_transition_happens(last_transition)
    sc.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_tls_sharded_cluster_certs_prefix
@mark.xfail(reason="Disabling security.tls.enabled does not disable TLS when security.tls.secretRef.prefix is set")
def test_sharded_cluster_has_connectivity_without_tls(sc: MongoDB):
    service_names = get_mongos_service_names(sc)
    tester = sc.tester(use_ssl=False, service_names=service_names)
    tester.assert_connectivity(opts=[{"serverSelectionTimeoutMs": 30000}], attempts=1)


@mark.e2e_tls_sharded_cluster_certs_prefix
def test_sharded_cluster_with_allow_tls(sc: MongoDB):
    sc["spec"]["security"]["tls"]["enabled"] = True

    additional_mongod_config = {
        "additionalMongodConfig": {
            "net": {
                "tls": {
                    "mode": "allowTLS",
                }
            }
        }
    }

    sc["spec"]["mongos"] = additional_mongod_config
    sc["spec"]["shard"] = additional_mongod_config
    sc["spec"]["configSrv"] = additional_mongod_config

    sc.update()
    sc.assert_reaches_phase(Phase.Running, timeout=1200)

    automation_config = KubernetesTester.get_automation_config()

    tls_modes = [
        process.get("args2_6", {}).get("net", {}).get("tls", {}).get("mode")
        for process in automation_config["processes"]
    ]

    # 3 mongod + 3 configSrv + 2 mongos = 8 processes
    assert len(tls_modes) == 8
    tls_modes_set = set(tls_modes)
    # all processes should have the same allowTLS value
    assert len(tls_modes_set) == 1
    assert tls_modes_set.pop() == "allowTLS"


@mark.e2e_tls_sharded_cluster_certs_prefix
@skip_if_local
def test_sharded_cluster_has_connectivity_with_tls_with_allow_tls_mode(sc: MongoDB, ca_path: str):
    service_names = get_mongos_service_names(sc)
    tester = sc.tester(ca_path=ca_path, use_ssl=True, service_names=service_names)
    tester.assert_connectivity()


@mark.e2e_tls_sharded_cluster_certs_prefix
@skip_if_local
def test_sharded_cluster_has_connectivity_without_tls_with_allow_tls_mode(sc: MongoDB):
    service_names = get_mongos_service_names(sc)
    tester = sc.tester(use_ssl=False, service_names=service_names)
    tester.assert_connectivity()
