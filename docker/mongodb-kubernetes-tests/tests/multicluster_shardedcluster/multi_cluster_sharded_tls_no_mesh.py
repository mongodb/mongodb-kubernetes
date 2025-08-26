import logging

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
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.mongotester import with_tls
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import (
    get_central_cluster_client,
    get_member_cluster_clients,
    get_member_cluster_names,
    update_coredns_hosts,
)
from tests.shardedcluster.conftest import (
    enable_multi_cluster_deployment,
    get_dns_hosts_for_external_access,
    setup_external_access,
)

MDB_RESOURCE = "sharded-cluster-custom-certs"
SUBJECT = {"organizations": ["MDB Tests"], "organizationalUnits": ["Servers"]}
SERVER_SETS = frozenset(
    [
        SetPropertiesMultiCluster(MDB_RESOURCE + "-0", MDB_RESOURCE + "-sh", 3, 3),
        SetPropertiesMultiCluster(MDB_RESOURCE + "-config", MDB_RESOURCE + "-cs", 3, 3),
        SetPropertiesMultiCluster(MDB_RESOURCE + "-mongos", MDB_RESOURCE + "-svc", 3, 3),
    ]
)


@fixture(scope="module")
def all_certs(issuer, namespace, sharded_cluster: MongoDB) -> None:
    """Generates all required TLS certificates: Servers and Client/Member."""

    for server_set in SERVER_SETS:
        # TODO: Think about optimizing this. For simplicity, we enable each cert to be valid for each Service
        # This way, all components can talk to each other. We may want to be more strict here and enable
        # communication between the same components only.
        service_fqdns = [
            external_address[1]
            for external_address in get_dns_hosts_for_external_access(
                resource=sharded_cluster, cluster_member_list=get_member_cluster_names()
            )
        ]

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


@fixture(scope="module")
def sharded_cluster(
    namespace: str,
    issuer_ca_configmap: str,
) -> MongoDB:
    mdb: MongoDB = MongoDB.from_yaml(
        _fixture("test-tls-base-sc-require-ssl.yaml"),
        name=MDB_RESOURCE,
        namespace=namespace,
    )
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

    enable_multi_cluster_deployment(resource=mdb)
    setup_external_access(resource=mdb)
    mdb.set_architecture_annotation()

    return mdb


@mark.e2e_multi_cluster_sharded_tls_no_mesh
def test_update_coredns(cluster_clients: dict[str, kubernetes.client.ApiClient], sharded_cluster: MongoDB):
    hosts = get_dns_hosts_for_external_access(resource=sharded_cluster, cluster_member_list=get_member_cluster_names())
    for cluster_name, cluster_api in cluster_clients.items():
        update_coredns_hosts(hosts, cluster_name, api_client=cluster_api)


@mark.e2e_multi_cluster_sharded_tls_no_mesh
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_sharded_tls_no_mesh
def test_deploy_certs(all_certs, agent_certs):
    logging.info(f"Certificates deployed successfully")


@mark.e2e_multi_cluster_sharded_tls_no_mesh
def test_sharded_cluster_with_prefix_gets_to_running_state(sharded_cluster: MongoDB):
    sharded_cluster.update()
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1400)


# TODO: (slaskawi) clearly the client tries to connect to mongos without TLS (we can see this in the logs).
# The Server rejects the connection (also we can see this in the logs) and everything ends with a timeout. Why?
# # Testing connectivity with External Access requires using the same DNS as deployed in Kube within
# # test_update_coredns. There's no easy way to set it up locally.
# @skip_if_local()
# @mark.e2e_multi_cluster_sharded_tls_no_mesh
# def test_shards_were_configured_and_accessible(sharded_cluster: MongoDB, ca_path: str):
#     hosts = get_dns_hosts_for_external_access(resource=sharded_cluster, cluster_member_list=get_member_cluster_names())
#     mongos_hostnames = [item[1] for item in hosts if "mongos" in item[1]]
#     # It's not obvious, but under the covers using Services and External Domain will ensure the tester respects
#     # the supplied hosts (and only them).
#     tester = sharded_cluster.tester(service_names=mongos_hostnames)
#     tester.assert_connectivity(opts=[with_tls(use_tls=True, ca_path=ca_path)])
