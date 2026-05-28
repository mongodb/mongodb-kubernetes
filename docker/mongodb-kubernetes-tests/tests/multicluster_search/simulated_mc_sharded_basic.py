"""Simulated multi-cluster MongoDBSearch e2e with a SHARDED external source.

Topology mirrors `simulated_mc_basic.py` (3 operators: 1 central + 2 simulated-MC),
but the external source is a `topology: MultiCluster` sharded MongoDB whose shards,
configsrv and mongos are distributed across both member kind clusters (q3 pattern,
adapted for the simulated-MC Search side):

    central operator (kind-e2e-operator):
        watches MongoDB / opsmanagers / MongoDBUser / MongoDBMultiCluster
        owns the MC sharded MongoDB CR and its automation across both members.

    simulated-MC operators (kind-e2e-cluster-1, kind-e2e-cluster-2):
        each watches mongodbsearch only, identity = OPERATOR_CLUSTER_NAME env var,
        per-cluster LocalizeToCluster collapse selects spec.clusters[i] slice and
        materialises per-(cluster, shard) mongot StatefulSets / Services / ConfigMaps
        / per-shard proxy Services + a per-cluster Envoy LB Deployment.

Each member's API server receives THE SAME MongoDBSearch CR (spec.clusters lists
every cluster, spec.source.external.shardedCluster lists every cluster's mongos in
its `router.hosts` and every cluster's per-shard mongod FQDN in each `shards[i].hosts`).
The simulated operator on each member projects to its own slice.

Each member cluster has its own local mongos, so data-plane queries dial that
local mongos with `directConnection=true`. The final test phases mongorestore
`sample_mflix`, hash-shard the `movies` collection across the 2 shards, create a
canonical `$search` index, and assert the same query returns rows via each
cluster's local mongos.
"""

from __future__ import annotations

import os
from typing import Dict, List, Tuple

import kubernetes
import pymongo.errors
from kubernetes.client import CoreV1Api
from kubetester import create_or_update_secret, read_secret, try_load
from kubetester.certs import create_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod.mongodb_tools_pod import ToolsPod
from tests.common.search import search_resource_names
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_tester import SearchTester
from tests.common.search.sharded_search_helper import (
    create_issuer_ca,
    create_lb_certificates,
    create_per_shard_search_tls_certs,
)
from tests.conftest import get_issuer_ca_filepath
from tests.multicluster.conftest import cluster_spec_list

logger = test_logger.get_test_logger(__name__)

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

MDB_RESOURCE_NAME = "mdb-mc-sim-sh"
MDBS_RESOURCE_NAME = "mdb-mc-sim-sh-search"

# Multi-cluster sharded source: 2 shards, 1 mongod-per-shard-per-cluster,
# 1 configsrv-per-cluster, 1 mongos-per-cluster. Two member clusters.
SHARD_COUNT = 2
MEMBERS_PER_CLUSTER: List[int | None] = [1, 1]
CONFIG_SRV_PER_CLUSTER: List[int | None] = [1, 1]
MONGOS_PER_CLUSTER: List[int | None] = [1, 1]

MONGOT_REPLICAS_PER_CLUSTER = 1
ENVOY_LB_REPLICAS = 1
ENVOY_PROXY_PORT = 27028
SIMULATED_OPERATOR_NAME = "mongodb-kubernetes-operator-simulated"

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"

USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"

# --- TLS-everywhere constants ----------------------------------------------
# Mongot/LB server+client cert prefix on the MongoDBSearch CR.
MDBS_TLS_CERT_PREFIX = "certs"
# CA ConfigMap (key: ca-pem / mms-ca.crt) consumed by the source MongoDB TLS spec
# and CA Secret (key: ca.crt) consumed by spec.source.external.tls.ca on the
# MongoDBSearch CR. create_issuer_ca writes both with the same name.
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"
# Source sharded cluster cert secret prefix. The MongoDB controller derives the
# per-component secret name as `{prefix}-{name}-{shardIdx}-cert` for shards,
# `{prefix}-{name}-config-cert` for configsrv, `{prefix}-{name}-mongos-cert` for
# mongos. Per-component certs (q3 pattern) — not a single bundle.
SOURCE_CERT_PREFIX = "clustercert"

SEARCH_INDEX_READY_TIMEOUT = 300
SEARCH_QUERY_RETRY_TIMEOUT = 90


def _idx(mcc: MultiClusterClient) -> int:
    assert mcc.cluster_index is not None, f"cluster_index unset on {mcc.cluster_name!r}"
    return mcc.cluster_index


def _install_simulated_operator(
    namespace: str,
    base_helm_args: Dict[str, str],
    mcc: MultiClusterClient,
) -> Operator:
    """Install a simulated-MC operator watching mongodbsearch only.
    Uses a DISTINCT operator.name so Helm renders fresh SA+Role+RoleBinding with
    mongodbsearch RBAC (the kube-config-creation-tool SA's Role lacks search perms).
    """
    os.environ["HELM_KUBECONTEXT"] = mcc.cluster_name

    helm_args = dict(base_helm_args)
    # multiCluster.clusters gates a kube-config-volume mount whose Secret is never created in this mode.
    for k in (
        "operator.watchNamespace",
        "multiCluster.clusters",
        "multiCluster.kubeConfigSecretName",
        "multiCluster.clusterClientTimeout",
        "multiCluster.performFailover",
    ):
        helm_args.pop(k, None)

    helm_args["operator.name"] = SIMULATED_OPERATOR_NAME
    helm_args["operator.createOperatorServiceAccount"] = "true"
    # MongoDB-resource-pod SAs are pre-created by run_kube_config_creation_tool — Helm 3
    # ownership-metadata check fails if we re-render them.
    helm_args["operator.createResourcesServiceAccountsAndRoles"] = "false"
    helm_args["operator.clusterIdentity.clusterName"] = mcc.cluster_name
    helm_args["operator.watchedResources"] = "{mongodbsearch}"

    operator = Operator(
        namespace=namespace,
        name=SIMULATED_OPERATOR_NAME,
        helm_args=helm_args,
        api_client=mcc.api_client,
    ).upgrade(multi_cluster=True)
    logger.info(
        f"installed simulated-MC operator in {mcc.cluster_name} "
        f"(identity={mcc.cluster_name}, name={SIMULATED_OPERATOR_NAME}, watches=mongodbsearch only)"
    )
    return operator


def _replicate_mongot_user_password_to_members(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
) -> None:
    """Replicate the password Secret so simulated-MC operators' source.passwordSecretRef resolves locally."""
    member_cluster_clients = member_cluster_clients[:2]
    pwd_secret_name = f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password"
    src = read_secret(namespace, pwd_secret_name, api_client=central_cluster_client)
    for mcc in member_cluster_clients:
        create_or_update_secret(namespace, pwd_secret_name, src, api_client=mcc.api_client)
        logger.info(f"replicated {pwd_secret_name} to {mcc.cluster_name}")


def _per_cluster_mongos_search_tester(
    namespace: str,
    cluster_index: int,
    username: str,
    password: str,
) -> SearchTester:
    """SearchTester pinned to a specific cluster's mongos via its per-pod headless Service.

    `directConnection=true` keeps the driver from discovering the other cluster's mongos
    so the query stays local to `cluster_index` (matches the q3 sharded MC pattern).
    """
    mongos_host = f"{MDB_RESOURCE_NAME}-mongos-{cluster_index}-0-svc.{namespace}.svc.cluster.local:27017"
    conn_str = f"mongodb://{username}:{password}@{mongos_host}/?directConnection=true&authSource=admin"
    return SearchTester(conn_str, use_ssl=True, ca_path=get_issuer_ca_filepath())


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@fixture(scope="module")
def central_mc_operator(
    namespace: str,
    central_cluster_name: str,
    multi_cluster_operator_installation_config: dict,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    member_cluster_names: List[str],
) -> Operator:
    """Central MC operator scoped to MongoDB/User/MultiCluster + mongodbsearch.

    The MongoDB ShardedCluster reconciler queries MongoDBSearch CRs via a field
    index (`field:mdbsearch-for-mongodbresourceref-index`). That index is only
    registered when mongodbsearch is included in `operator.watchedResources`.
    Without it the central operator's MongoDB CR reconcile fails with:
        "Failed to list MongoDBSearch resources: Index with name
         field:mdbsearch-for-mongodbresourceref-index does not exist"
    Including mongodbsearch registers the indexer; the controller starts idle
    because the test only applies MongoDBSearch CRs to MEMBER cluster APIs.
    """
    from tests.conftest import (
        MULTI_CLUSTER_OPERATOR_NAME,
        _install_multi_cluster_operator,
        local_operator,
        run_kube_config_creation_tool,
    )

    member_cluster_clients = member_cluster_clients[:2]
    member_cluster_names = member_cluster_names[:2]

    os.environ["HELM_KUBECONTEXT"] = central_cluster_name

    if not local_operator():
        run_kube_config_creation_tool(member_cluster_names, namespace, namespace, member_cluster_names)

    return _install_multi_cluster_operator(
        namespace,
        multi_cluster_operator_installation_config,
        central_cluster_client,
        member_cluster_clients,
        {
            "operator.name": MULTI_CLUSTER_OPERATOR_NAME,
            "operator.createOperatorServiceAccount": "false",
            "operator.watchedResources": "{mongodb,opsmanagers,mongodbusers,mongodbcommunity,mongodbmulticluster,mongodbsearch}",
        },
        central_cluster_name,
    )


@fixture(scope="module")
def ca_configmap(
    issuer_ca_filepath: str,
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
) -> str:
    """Issuer CA written to central AND every member cluster.

    create_issuer_ca writes both a ConfigMap (key: ca-pem/mms-ca.crt) and a Secret
    (key: ca.crt) with the same name. The MongoDB controller's TLS spec references
    the ConfigMap on central; the simulated-MC search operators reference the Secret
    half on each member; mongot pods on members need to verify the source MongoDB
    TLS cert at runtime.
    """
    name = create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)
    for mcc in member_cluster_clients[:2]:
        create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME, api_client=mcc.api_client)
        logger.info(f"CA ConfigMap/Secret {CA_CONFIGMAP_NAME} created in cluster {mcc.cluster_name}")
    return name


@fixture(scope="module")
def mdb(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
    ca_configmap: str,
) -> MongoDB:
    """MultiCluster sharded MongoDB source with TLS+SCRAM, applied to the central cluster.

    Per-shard `mongotHost` / `searchIndexManagementHostAndPort` point at the
    per-shard proxy Service on cluster-0 (Envoy is namespace-scoped and reachable
    from cluster-1 over Istio east-west; q3 uses the same routing). The mongos
    cluster-level endpoint targets cluster-0's `*-search-0-proxy-svc`.
    """
    member_cluster_names = member_cluster_names[:2]
    resource = MongoDB.from_yaml(
        yaml_fixture("simulated-mc-sharded-mongodb.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )

    resource["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, MEMBERS_PER_CLUSTER)
    resource["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, CONFIG_SRV_PER_CLUSTER)
    resource["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, MONGOS_PER_CLUSTER)

    # MC MongoDB has no per-cluster additionalMongodConfig; both clusters get the
    # same setParameter values. mongot lookups funnel through cluster-0's Envoy
    # (Istio east-west handles cluster-1 → cluster-0 routing). Per-cluster QUERY
    # paths still work because each cluster has its own local mongos that the
    # test dials directly with `directConnection=true`.
    cluster_idx = 0
    cluster_level_endpoint = (
        f"{search_resource_names.mc_proxy_svc_fqdn(MDBS_RESOURCE_NAME, namespace, cluster_idx)}:{ENVOY_PROXY_PORT}"
    )

    def _shard_proxy_endpoint(shard_name: str) -> str:
        proxy_name = search_resource_names.shard_proxy_service_name(MDBS_RESOURCE_NAME, shard_name, cluster_idx)
        return f"{proxy_name}.{namespace}.svc.cluster.local:{ENVOY_PROXY_PORT}"

    base_search_set_parameter = {
        "skipAuthenticationToSearchIndexManagementServer": False,
        "skipAuthenticationToMongot": False,
        "searchTLSMode": "requireTLS",
        "useGrpcForSearch": True,
    }

    resource["spec"]["shardOverrides"] = [
        {
            "shardNames": [f"{MDB_RESOURCE_NAME}-{shard_idx}"],
            "additionalMongodConfig": {
                "setParameter": {
                    **base_search_set_parameter,
                    "mongotHost": _shard_proxy_endpoint(f"{MDB_RESOURCE_NAME}-{shard_idx}"),
                    "searchIndexManagementHostAndPort": _shard_proxy_endpoint(f"{MDB_RESOURCE_NAME}-{shard_idx}"),
                },
            },
        }
        for shard_idx in range(SHARD_COUNT)
    ]

    resource["spec"]["mongos"]["additionalMongodConfig"] = {
        "setParameter": {
            **base_search_set_parameter,
            "mongotHost": cluster_level_endpoint,
            "searchIndexManagementHostAndPort": cluster_level_endpoint,
        },
    }
    resource["spec"]["shard"]["additionalMongodConfig"] = {
        "setParameter": base_search_set_parameter,
    }

    resource["spec"]["security"] = {
        "certsSecretPrefix": SOURCE_CERT_PREFIX,
        "tls": {"ca": ca_configmap},
        "authentication": {"enabled": True, "modes": ["SCRAM"]},
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


def _build_user(
    yaml_filename: str,
    name: str,
    username: str,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(yaml_fixture(yaml_filename), namespace=namespace, name=name)
    if not try_load(resource):
        resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME
        resource["spec"]["username"] = username
        resource["spec"]["passwordSecretKeyRef"]["name"] = f"{name}-password"
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource


@fixture(scope="module")
def admin_user(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBUser:
    return _build_user(
        "mongodbuser-mdb-admin.yaml", ADMIN_USER_NAME, ADMIN_USER_NAME, namespace, central_cluster_client
    )


@fixture(scope="module")
def mdb_user(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBUser:
    return _build_user("mongodbuser-mdb-user.yaml", USER_NAME, USER_NAME, namespace, central_cluster_client)


@fixture(scope="module")
def mongot_user(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBUser:
    return _build_user(
        "mongodbuser-search-sync-source-user.yaml",
        f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}",
        MONGOT_USER_NAME,
        namespace,
        central_cluster_client,
    )


@fixture(scope="module")
def per_cluster_mdbs_search(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
    ca_configmap: str,
) -> List[Tuple[MultiClusterClient, MongoDBSearch]]:
    """Same CR (spec.clusters lists all clusters, spec.source.external.shardedCluster set)
    applied to each member's API; each operator's LocalizeToCluster collapse selects its
    own slice and fans out per-(cluster, shard) mongot resources.
    """
    member_cluster_clients = member_cluster_clients[:2]
    results: List[Tuple[MultiClusterClient, MongoDBSearch]] = []

    clusters_spec = [
        {
            "clusterName": mcc.cluster_name,
            "clusterIndex": _idx(mcc),
            "replicas": MONGOT_REPLICAS_PER_CLUSTER,
        }
        for mcc in member_cluster_clients
    ]

    # MC sharded source: router/hosts lists every cluster's local mongos pod-FQDN;
    # each shard's `hosts` lists every cluster's mongod pod-FQDN for that shard.
    # Per-pod headless Services (`<sts>-<clusterIdx>-<podIdx>-svc`) are reachable
    # cross-cluster via Istio east-west (the per-cluster `<sts>-<clusterIdx>-svc`
    # is not). Mirrors the q3 pattern.
    router_hosts = [
        f"{MDB_RESOURCE_NAME}-mongos-{cluster_idx}-{pod_idx}-svc.{namespace}.svc.cluster.local:27017"
        for cluster_idx, n_mongos in enumerate(MONGOS_PER_CLUSTER)
        if n_mongos
        for pod_idx in range(n_mongos)
    ]
    shards = [
        {
            "shardName": f"{MDB_RESOURCE_NAME}-{shard_idx}",
            "hosts": [
                f"{MDB_RESOURCE_NAME}-{shard_idx}-{cluster_idx}-0-svc.{namespace}.svc.cluster.local:27017"
                for cluster_idx, n_members in enumerate(MEMBERS_PER_CLUSTER)
                if n_members
            ],
        }
        for shard_idx in range(SHARD_COUNT)
    ]

    for mcc in member_cluster_clients:
        mdbs = MongoDBSearch.from_yaml(
            yaml_fixture("simulated-mc-search.yaml"),
            name=MDBS_RESOURCE_NAME,
            namespace=namespace,
        )
        mdbs["spec"]["clusters"] = clusters_spec
        mdbs["spec"]["loadBalancer"] = {
            "managed": {
                "replicas": ENVOY_LB_REPLICAS,
                # Per-cluster, per-shard externalHostname template — operator
                # substitutes {clusterIndex} and {shardName} per resource.
                "externalHostname": (
                    f"{MDBS_RESOURCE_NAME}-search-{{clusterIndex}}-{{shardName}}-proxy-svc"
                    f".{namespace}.svc.cluster.local"
                ),
            },
        }
        # Replace the fixture's hostAndPorts default with shardedCluster source.
        mdbs["spec"]["source"] = {
            "username": MONGOT_USER_NAME,
            "passwordSecretRef": {
                "name": f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password",
                "key": "password",
            },
            "external": {
                "shardedCluster": {
                    "router": {"hosts": router_hosts},
                    "shards": shards,
                },
                "tls": {"ca": {"name": ca_configmap}},
            },
        }
        mdbs["spec"]["security"] = {
            "tls": {"certsSecretPrefix": MDBS_TLS_CERT_PREFIX},
        }
        mdbs.api = kubernetes.client.CustomObjectsApi(mcc.api_client)
        try_load(mdbs)
        results.append((mcc, mdbs))

    return results


# ---------------------------------------------------------------------------
# Setup
# ---------------------------------------------------------------------------


@mark.e2e_search_simulated_mc_sharded_basic
def test_install_central_mc_operator(central_mc_operator: Operator):
    central_mc_operator.assert_is_running()


@mark.e2e_search_simulated_mc_sharded_basic
def test_install_simulated_operators_per_member(
    namespace: str,
    central_mc_operator: Operator,
    multi_cluster_operator_installation_config: dict,
    member_cluster_clients: List[MultiClusterClient],
):
    member_cluster_clients = member_cluster_clients[:2]
    base_helm_args = dict(multi_cluster_operator_installation_config)
    for mcc in member_cluster_clients:
        operator = _install_simulated_operator(namespace, base_helm_args, mcc)
        operator.assert_is_running()


@mark.e2e_search_simulated_mc_sharded_basic
def test_install_source_tls_certificates(
    namespace: str,
    multi_cluster_issuer: str,
    central_cluster_client: kubernetes.client.ApiClient,
    mdb: MongoDB,
):
    """Source MongoDB per-component TLS certs (q3 pattern — per-shard, per-configsrv,
    per-mongos with `replicas_cluster_distribution`). Certs are written to the central
    cluster; the central MongoDB controller replicates them to members.
    """

    def _issue(component_resource: str, secret_name: str, distribution: List[int | None]):
        create_tls_certs(
            issuer=multi_cluster_issuer,
            namespace=namespace,
            resource_name=component_resource,
            replicas_cluster_distribution=distribution,
            secret_name=secret_name,
            api_client=central_cluster_client,
        )

    for shard_idx in range(SHARD_COUNT):
        _issue(
            component_resource=f"{MDB_RESOURCE_NAME}-{shard_idx}",
            secret_name=f"{SOURCE_CERT_PREFIX}-{MDB_RESOURCE_NAME}-{shard_idx}-cert",
            distribution=MEMBERS_PER_CLUSTER,
        )
    _issue(
        component_resource=f"{MDB_RESOURCE_NAME}-config",
        secret_name=f"{SOURCE_CERT_PREFIX}-{MDB_RESOURCE_NAME}-config-cert",
        distribution=CONFIG_SRV_PER_CLUSTER,
    )
    _issue(
        component_resource=f"{MDB_RESOURCE_NAME}-mongos",
        secret_name=f"{SOURCE_CERT_PREFIX}-{MDB_RESOURCE_NAME}-mongos-cert",
        distribution=MONGOS_PER_CLUSTER,
    )
    logger.info(f"Source sharded cluster per-component TLS certs created (prefix={SOURCE_CERT_PREFIX})")


@mark.e2e_search_simulated_mc_sharded_basic
def test_mongodb_running(mdb: MongoDB):
    mdb.update()
    # ignore_errors=True: under `topology: MultiCluster`, the central operator can
    # write a transient phase=Failed when the controller-runtime cached client lags
    # behind a successful STS Create on a member cluster (Get returns NotFound for
    # up to ~10s after Create). The next requeue picks up the freshly-synced cache.
    # See project_simulated_mc_search.md "STS visibility lag" for the diagnosis.
    mdb.assert_reaches_phase(Phase.Running, timeout=1800, ignore_errors=True)


@mark.e2e_search_simulated_mc_sharded_basic
def test_create_users(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    admin_user: MongoDBUser,
    mdb_user: MongoDBUser,
    mongot_user: MongoDBUser,
    member_cluster_clients: List[MultiClusterClient],
):
    member_cluster_clients = member_cluster_clients[:2]
    for user, pwd in (
        (admin_user, ADMIN_USER_PASSWORD),
        (mdb_user, USER_PASSWORD),
        (mongot_user, MONGOT_USER_PASSWORD),
    ):
        create_or_update_secret(
            namespace,
            name=user["spec"]["passwordSecretKeyRef"]["name"],
            data={"password": pwd},
            api_client=central_cluster_client,
        )
        user.update()

    admin_user.assert_reaches_phase(Phase.Updated, timeout=600)
    mdb_user.assert_reaches_phase(Phase.Updated, timeout=600)
    mongot_user.assert_reaches_phase(Phase.Updated, timeout=600)

    _replicate_mongot_user_password_to_members(namespace, central_cluster_client, member_cluster_clients)


@mark.e2e_search_simulated_mc_sharded_basic
def test_deploy_lb_certificates(
    namespace: str,
    multi_cluster_issuer: str,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):
    """Per-cluster, per-shard LB certs issued on central — SANs cover every cluster's
    per-shard proxy Service FQDN plus the cluster-level proxy Service for mongos.
    """
    member_cluster_clients = member_cluster_clients[:2]
    create_lb_certificates(
        namespace=namespace,
        issuer=multi_cluster_issuer,
        shard_count=SHARD_COUNT,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        tls_cert_prefix=MDBS_TLS_CERT_PREFIX,
        cluster_indexes=[_idx(mcc) for mcc in member_cluster_clients],
        api_client=central_cluster_client,
    )


@mark.e2e_search_simulated_mc_sharded_basic
def test_create_search_tls_certificates(
    namespace: str,
    multi_cluster_issuer: str,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):
    """Per-(cluster, shard) mongot TLS certs issued on central; replicated below.

    cert-manager is installed only on the central cluster, so all Certificate CRs
    land there; the next step copies the resulting Secrets to each member.
    """
    member_cluster_clients = member_cluster_clients[:2]
    for mcc in member_cluster_clients:
        create_per_shard_search_tls_certs(
            namespace=namespace,
            issuer=multi_cluster_issuer,
            prefix=MDBS_TLS_CERT_PREFIX,
            shard_count=SHARD_COUNT,
            mdb_resource_name=MDB_RESOURCE_NAME,
            mdbs_resource_name=MDBS_RESOURCE_NAME,
            cluster_index=_idx(mcc),
            api_client=central_cluster_client,
        )
        logger.info(f"Per-shard Search TLS certs created on central for cluster_index={_idx(mcc)}")


@mark.e2e_search_simulated_mc_sharded_basic
def test_replicate_tls_to_members(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
):
    """Simulated-MC operators read only their own etcd — TLS material must be present locally."""
    member_cluster_clients = member_cluster_clients[:2]
    central_core = CoreV1Api(api_client=central_cluster_client)

    # Cluster-agnostic Secrets — same copy to every member cluster.
    shared_secret_names = [
        search_resource_names.lb_server_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX),
        search_resource_names.lb_client_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX),
    ]
    for secret_name in shared_secret_names:
        secret_type = central_core.read_namespaced_secret(name=secret_name, namespace=namespace).type or "Opaque"
        data = read_secret(namespace, secret_name, api_client=central_cluster_client)
        for mcc in member_cluster_clients:
            create_or_update_secret(namespace, secret_name, data, type=secret_type, api_client=mcc.api_client)
        logger.info(f"replicated Secret {secret_name} to {len(member_cluster_clients)} member cluster(s)")

    # Per-(cluster, shard) mongot certs — each cluster only needs its own per-shard certs.
    for mcc in member_cluster_clients:
        ci = _idx(mcc)
        for shard_idx in range(SHARD_COUNT):
            shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
            secret_name = search_resource_names.shard_tls_cert_name(
                MDBS_RESOURCE_NAME, shard_name, MDBS_TLS_CERT_PREFIX, cluster_index=ci
            )
            secret_type = central_core.read_namespaced_secret(name=secret_name, namespace=namespace).type or "Opaque"
            data = read_secret(namespace, secret_name, api_client=central_cluster_client)
            create_or_update_secret(namespace, secret_name, data, type=secret_type, api_client=mcc.api_client)
        logger.info(f"replicated per-shard mongot Secrets to {mcc.cluster_name} (cluster_index={ci})")


# ---------------------------------------------------------------------------
# Search CR lifecycle
# ---------------------------------------------------------------------------


@mark.e2e_search_simulated_mc_sharded_basic
def test_operators_running(
    central_mc_operator: Operator,
    member_cluster_clients: List[MultiClusterClient],
    namespace: str,
):
    """Central + per-member simulated operators all running."""
    central_mc_operator.assert_is_running()
    for mcc in member_cluster_clients[:2]:
        Operator(namespace=namespace, name=SIMULATED_OPERATOR_NAME, api_client=mcc.api_client).assert_is_running()


@mark.e2e_search_simulated_mc_sharded_basic
def test_central_sharded_mongodb_running(mdb: MongoDB):
    """Final assertion that the MC sharded MongoDB source remained Running through setup."""
    mdb.load()
    actual = mdb.get_status_phase()
    assert actual == Phase.Running, f"MC sharded MongoDB {mdb.name} phase={actual}, expected Phase.Running"


@mark.e2e_search_simulated_mc_sharded_basic
def test_search_cr_reaches_running_per_cluster(
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    """Same MongoDBSearch CR applied to each member; each simulated operator reconciles
    its own LocalizeToCluster slice and stamps Running."""
    for _mcc, mdbs in per_cluster_mdbs_search:
        mdbs.update()
    for mcc, mdbs in per_cluster_mdbs_search:
        mdbs.assert_reaches_phase(Phase.Running, timeout=1200)
        logger.info(f"MongoDBSearch {mdbs.name} Running in {mcc.cluster_name}")


@mark.e2e_search_simulated_mc_sharded_basic
def test_per_cluster_sharded_resources_exist(
    namespace: str,
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    """Each member must materialise its own (cluster, shard) mongot STS / Svc / CM /
    proxy Svc plus a per-cluster Envoy LB Deployment. Resources on OTHER clusters
    must not leak into this cluster's API server.
    """
    for mcc, _mdbs in per_cluster_mdbs_search:
        ci = _idx(mcc)
        for shard_idx in range(SHARD_COUNT):
            shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
            sts_name = search_resource_names.shard_statefulset_name(MDBS_RESOURCE_NAME, shard_name, ci)
            svc_name = search_resource_names.shard_service_name(MDBS_RESOURCE_NAME, shard_name, ci)
            cm_name = search_resource_names.shard_configmap_name(MDBS_RESOURCE_NAME, shard_name, ci)
            proxy_svc_name = search_resource_names.shard_proxy_service_name(MDBS_RESOURCE_NAME, shard_name, ci)

            sts = mcc.read_namespaced_stateful_set(sts_name, namespace)
            assert sts.spec.replicas == MONGOT_REPLICAS_PER_CLUSTER, (
                f"[{mcc.cluster_name}] {sts_name} expected {MONGOT_REPLICAS_PER_CLUSTER} replicas, "
                f"got {sts.spec.replicas}"
            )
            mcc.read_namespaced_service(svc_name, namespace)
            mcc.read_namespaced_service(proxy_svc_name, namespace)
            mcc.read_namespaced_config_map(cm_name, namespace)
            logger.info(
                f"[{mcc.cluster_name}] (cluster_index={ci}, shard={shard_name}): "
                f"sts={sts_name} svc={svc_name} proxy={proxy_svc_name} cm={cm_name}"
            )

        envoy_dep_name = search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, ci)
        envoy_dep = mcc.apps_v1_api().read_namespaced_deployment(name=envoy_dep_name, namespace=namespace)
        assert envoy_dep.spec.replicas == ENVOY_LB_REPLICAS, (
            f"[{mcc.cluster_name}] {envoy_dep_name} expected {ENVOY_LB_REPLICAS} replicas, "
            f"got {envoy_dep.spec.replicas}"
        )
        logger.info(f"[{mcc.cluster_name}] envoy_lb={envoy_dep_name} (replicas={ENVOY_LB_REPLICAS})")


@mark.e2e_search_simulated_mc_sharded_basic
def test_no_cross_cluster_resource_leak(
    namespace: str,
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    """Resources owned by OTHER cluster indices must NOT exist on this cluster's API.

    This is the simulated-MC invariant: each operator only writes resources for its
    own LocalizeToCluster slice. A cross-cluster leak would mean the projection
    collapse didn't fire correctly.
    """
    all_cluster_indices = [_idx(mcc) for mcc, _ in per_cluster_mdbs_search]
    for mcc, _mdbs in per_cluster_mdbs_search:
        own_ci = _idx(mcc)
        for foreign_ci in all_cluster_indices:
            if foreign_ci == own_ci:
                continue
            for shard_idx in range(SHARD_COUNT):
                shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
                sts_name = search_resource_names.shard_statefulset_name(MDBS_RESOURCE_NAME, shard_name, foreign_ci)
                try:
                    mcc.read_namespaced_stateful_set(sts_name, namespace)
                except kubernetes.client.exceptions.ApiException as exc:
                    if exc.status == 404:
                        continue
                    raise
                else:
                    raise AssertionError(
                        f"[{mcc.cluster_name}] LEAK: foreign-cluster STS {sts_name} "
                        f"(cluster_index={foreign_ci}) found on cluster_index={own_ci}'s API"
                    )
        logger.info(f"[{mcc.cluster_name}] no foreign-cluster resources leaked into local API server")


@mark.e2e_search_simulated_mc_sharded_basic
def test_status_per_cluster_local_only(
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    """No cross-cluster status awareness in simulated-MC mode; each cluster's CR view
    reports its own phase based on its own LocalizeToCluster slice.
    """
    for mcc, mdbs in per_cluster_mdbs_search:
        mdbs.load()
        phase = mdbs.get_status_phase()
        assert phase == Phase.Running, f"[{mcc.cluster_name}] MongoDBSearch {mdbs.name} phase={phase}, expected Running"


# ---------------------------------------------------------------------------
# Data plane: per-cluster $search via local mongos (directConnection=true)
# ---------------------------------------------------------------------------


@mark.e2e_search_simulated_mc_sharded_basic
def test_restore_sample_database(namespace: str, tools_pod: ToolsPod):
    """Restore sample_mflix via mongorestore through cluster-0's local mongos.

    Once restored, the data is owned by the logical sharded cluster (one configsrv
    routing layer across both clusters), so cluster-1's mongos sees the same data.
    """
    tester = _per_cluster_mongos_search_tester(namespace, 0, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
    tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )


@mark.e2e_search_simulated_mc_sharded_basic
def test_shard_sample_collection(namespace: str):
    """Hash-shard sample_mflix.movies across the 2 shards so $search exercises every
    per-shard mongot in both clusters."""
    admin = _per_cluster_mongos_search_tester(namespace, 0, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
    admin.shard_and_distribute_collection("sample_mflix", "movies")


@mark.e2e_search_simulated_mc_sharded_basic
def test_create_search_index(namespace: str):
    """Create the canonical movies $search index and wait for it READY. Created once
    at the logical-cluster level; each per-shard mongot picks the definition up via
    its configsrv watch."""
    tester = _per_cluster_mongos_search_tester(namespace, 0, USER_NAME, USER_PASSWORD)
    movies = SampleMoviesSearchHelper(search_tester=tester)
    movies.create_search_index()
    tester.wait_for_search_indexes_ready(movies.db_name, movies.col_name, timeout=SEARCH_INDEX_READY_TIMEOUT)


@mark.e2e_search_simulated_mc_sharded_basic
def test_per_cluster_search_query(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    """$search via EACH cluster's local mongos — proves both clusters' Envoy +
    per-shard mongot data path returns rows. `directConnection=true` pins the
    driver to the per-cluster mongos so the query never leaves the cluster.
    """
    member_cluster_clients = member_cluster_clients[:2]
    for mcc in member_cluster_clients:
        cluster_index = _idx(mcc)
        tester = _per_cluster_mongos_search_tester(namespace, cluster_index, USER_NAME, USER_PASSWORD)
        movies = SampleMoviesSearchHelper(search_tester=tester)

        def execute_search(_ci: int = cluster_index) -> tuple:
            try:
                results = movies.text_search_movies("Star Wars")
                count = len(results)
                if count > 0:
                    titles = [r.get("title") for r in results[:5]]
                    logger.info(f"cluster_index={_ci}: $search returned {count} results: {titles}")
                    return True, f"cluster_index={_ci}: $search returned {count} results"
                return False, f"cluster_index={_ci}: $search returned no results"
            except pymongo.errors.PyMongoError as exc:
                return False, f"cluster_index={_ci}: $search error: {exc}"

        run_periodically(
            execute_search,
            timeout=SEARCH_QUERY_RETRY_TIMEOUT,
            sleep_time=5,
            msg=f"cluster_index={cluster_index}: $search via local mongos",
        )
