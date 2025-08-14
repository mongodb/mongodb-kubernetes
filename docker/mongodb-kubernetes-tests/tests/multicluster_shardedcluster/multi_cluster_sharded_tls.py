import kubernetes
from kubetester import try_load
from kubetester.certs import (
    SetPropertiesMultiCluster,
    generate_cert,
    get_agent_x509_subject,
    get_mongodb_x509_subject,
)
from kubetester.certs_mongodb_multi import create_multi_cluster_tls_certs
from kubetester.kubetester import fixture as _fixture
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import (
    get_central_cluster_client,
    get_member_cluster_clients,
    get_member_cluster_names,
)
from tests.multicluster.conftest import cluster_spec_list

MDB_RESOURCE = "sharded-cluster-custom-certs"
SUBJECT = {"organizations": ["MDB Tests"], "organizationalUnits": ["Servers"]}
SERVER_SETS = frozenset(
    [
        SetPropertiesMultiCluster(MDB_RESOURCE + "-0", MDB_RESOURCE + "-sh", 3, 2),
        SetPropertiesMultiCluster(MDB_RESOURCE + "-config", MDB_RESOURCE + "-cs", 3, 2),
        SetPropertiesMultiCluster(MDB_RESOURCE + "-mongos", MDB_RESOURCE + "-svc", 2, 2),
    ]
)


@fixture(scope="module")
def all_certs(issuer, namespace) -> None:
    """Generates all required TLS certificates: Servers and Client/Member."""

    for server_set in SERVER_SETS:
        service_fqdns = []
        for cluster_idx in range(server_set.number_of_clusters):
            for pod_idx in range(server_set.replicas):
                service_fqdns.append(f"{server_set.name}-{cluster_idx}-{pod_idx}-svc.{namespace}.svc.cluster.local")

        create_multi_cluster_tls_certs(
            multi_cluster_issuer=issuer,
            central_cluster_client=get_central_cluster_client(),
            member_clients=get_member_cluster_clients(),
            secret_name="prefix-" + server_set.name + "-cert",
            mongodb_multi=None,
            namespace=namespace,
            additional_domains=None,
            service_fqdns=service_fqdns,
            clusterwide=False,
            spec=get_mongodb_x509_subject(namespace),
        )

        create_multi_cluster_tls_certs(
            multi_cluster_issuer=issuer,
            central_cluster_client=get_central_cluster_client(),
            member_clients=get_member_cluster_clients(),
            secret_name="prefix-" + server_set.name + "-clusterfile",
            mongodb_multi=None,
            namespace=namespace,
            additional_domains=None,
            service_fqdns=service_fqdns,
            clusterwide=False,
            spec=get_mongodb_x509_subject(namespace),
        )


@fixture(scope="module")
def agent_certs(
    namespace: str,
    issuer: str,
):
    spec = get_agent_x509_subject(namespace)
    return generate_cert(
        namespace=namespace,
        pod="tmp",
        dns="",
        issuer=issuer,
        spec=spec,
        multi_cluster_mode=True,
        api_client=get_central_cluster_client(),
        secret_name=f"prefix-{MDB_RESOURCE}-agent-certs",
    )


@fixture(scope="function")
def sharded_cluster(
    namespace: str,
    all_certs,
    agent_certs,
    issuer_ca_configmap: str,
) -> MongoDB:
    mdb: MongoDB = MongoDB.from_yaml(
        _fixture("test-tls-base-sc-require-ssl.yaml"),
        name=MDB_RESOURCE,
        namespace=namespace,
    )
    mdb.api = kubernetes.client.CustomObjectsApi(get_central_cluster_client())
    if try_load(mdb):
        return mdb

    mdb["spec"]["security"] = {
        "authentication": {
            "enabled": True,
            "modes": ["X509"],
            "agents": {"mode": "X509"},
            "internalCluster": "X509",
        },
        "tls": {
            "enabled": True,
            "ca": issuer_ca_configmap,
        },
        "certsSecretPrefix": "prefix",
    }
    mdb["spec"]["mongodsPerShardCount"] = 0
    mdb["spec"]["mongosCount"] = 0
    mdb["spec"]["configServerCount"] = 0
    mdb["spec"]["topology"] = "MultiCluster"
    mdb["spec"]["shard"] = {}
    mdb["spec"]["configSrv"] = {}
    mdb["spec"]["mongos"] = {}
    mdb["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [3, 1])
    mdb["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [3, 1])
    mdb["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 1])

    mdb.set_architecture_annotation()

    return mdb


@mark.e2e_multi_cluster_sharded_tls
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_sharded_tls
def test_sharded_cluster_with_prefix_gets_to_running_state(sharded_cluster: MongoDB):
    sharded_cluster.update()
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1200)
