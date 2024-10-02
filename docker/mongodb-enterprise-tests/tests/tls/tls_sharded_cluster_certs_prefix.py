from kubetester import try_load
from kubetester.certs import Certificate, SetProperties, create_mongodb_tls_certs
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as _fixture
from kubetester.kubetester import is_static_containers_architecture, skip_if_local
from kubetester.mongodb import MongoDB, Phase
from pytest import fixture, mark

MDB_RESOURCE = "sharded-cluster-custom-certs"
SUBJECT = {"organizations": ["MDB Tests"], "organizationalUnits": ["Servers"]}
SERVER_SETS = frozenset(
    [
        SetProperties(MDB_RESOURCE + "-0", MDB_RESOURCE + "-sh", 3),
        SetProperties(MDB_RESOURCE + "-config", MDB_RESOURCE + "-cs", 3),
        SetProperties(MDB_RESOURCE + "-mongos", MDB_RESOURCE + "-svc", 2),
    ]
)


@fixture(scope="module")
def all_certs(issuer, namespace) -> None:
    """Generates all required TLS certificates: Servers and Client/Member."""
    spec = {
        "subject": SUBJECT,
        "usages": ["server auth", "client auth"],
    }

    for server_set in SERVER_SETS:
        create_mongodb_tls_certs(
            issuer,
            namespace,
            server_set.name,
            "prefix-" + server_set.name + "-cert",
            server_set.replicas,
            server_set.service,
            spec,
        )


@fixture(scope="module")
def sharded_cluster(
    namespace: str,
    all_certs,
    issuer_ca_configmap: str,
) -> MongoDB:
    mdb: MongoDB = MongoDB.from_yaml(
        _fixture("test-tls-base-sc-require-ssl.yaml"),
        name=MDB_RESOURCE,
        namespace=namespace,
    )
    mdb["spec"]["security"] = {
        "tls": {
            "enabled": True,
            "ca": issuer_ca_configmap,
        },
        "certsSecretPrefix": "prefix",
    }
    mdb.set_architecture_annotation()

    try_load(mdb)
    return mdb


@mark.e2e_tls_sharded_cluster_certs_prefix
def test_sharded_cluster_with_prefix_gets_to_running_state(sharded_cluster: MongoDB):
    sharded_cluster.update()
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_tls_sharded_cluster_certs_prefix
@skip_if_local
def test_sharded_cluster_has_connectivity_with_tls(sharded_cluster: MongoDB, ca_path: str):
    tester = sharded_cluster.tester(ca_path=ca_path, use_ssl=True)
    tester.assert_connectivity()


@mark.e2e_tls_sharded_cluster_certs_prefix
@skip_if_local
def test_sharded_cluster_has_no_connectivity_without_tls(sharded_cluster: MongoDB):
    tester = sharded_cluster.tester(use_ssl=False)
    tester.assert_no_connection()


@mark.e2e_tls_sharded_cluster_certs_prefix
def test_rotate_tls_certificate(sharded_cluster: MongoDB, namespace: str):
    # update the shard cert
    cert = Certificate(name=f"prefix-{MDB_RESOURCE}-0-cert", namespace=namespace).load()
    cert["spec"]["dnsNames"].append("foo")
    cert.update()

    sharded_cluster.assert_abandons_phase(Phase.Running)
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=800)


@mark.e2e_tls_sharded_cluster_certs_prefix
def test_disable_tls(sharded_cluster: MongoDB):

    last_transition = sharded_cluster.get_status_last_transition_time()
    sharded_cluster.load()
    sharded_cluster["spec"]["security"]["tls"]["enabled"] = False
    sharded_cluster.update()

    sharded_cluster.assert_state_transition_happens(last_transition)
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_tls_sharded_cluster_certs_prefix
@mark.xfail(reason="Disabling security.tls.enabled does not disable TLS when security.tls.secretRef.prefix is set")
def test_sharded_cluster_has_connectivity_without_tls(sharded_cluster: MongoDB):
    tester = sharded_cluster.tester(use_ssl=False)
    tester.assert_connectivity(opts=[{"serverSelectionTimeoutMs": 30000}])


@mark.e2e_tls_sharded_cluster_certs_prefix
def test_sharded_cluster_with_allow_tls(sharded_cluster: MongoDB):
    sharded_cluster.load()

    sharded_cluster["spec"]["security"]["tls"]["enabled"] = True

    additional_mongod_config = {
        "additionalMongodConfig": {
            "net": {
                "tls": {
                    "mode": "allowTLS",
                }
            }
        }
    }

    sharded_cluster["spec"]["mongos"] = additional_mongod_config
    sharded_cluster["spec"]["shard"] = additional_mongod_config
    sharded_cluster["spec"]["configSrv"] = additional_mongod_config

    sharded_cluster.update()
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1200)

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
def test_sharded_cluster_has_connectivity_with_tls_with_allow_tls_mode(sharded_cluster: MongoDB, ca_path: str):
    tester = sharded_cluster.tester(ca_path=ca_path, use_ssl=True)
    tester.assert_connectivity()


@mark.e2e_tls_sharded_cluster_certs_prefix
@skip_if_local
def test_sharded_cluster_has_connectivity_without_tls_with_allow_tls_mode(
    sharded_cluster: MongoDB,
):
    tester = sharded_cluster.tester(use_ssl=False)
    tester.assert_connectivity()
