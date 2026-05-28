"""Simulated multi-cluster MongoDBSearch e2e with a SHARDED external source.

Topology mirrors `simulated_mc_basic.py` (3 operators: 1 central + 2 simulated-MC),
but the external source is a single-cluster sharded MongoDB deployed on the central
cluster instead of a cross-cluster MongoDBMulti ReplicaSet:

    central operator (kind-e2e-operator):
        watches MongoDB / opsmanagers / MongoDBUser / MongoDBMultiCluster
        hosts the sharded MongoDB source (2 shards × 1 mongod, 1 mongos, 1 configsrv)

    simulated-MC operators (kind-e2e-cluster-1, kind-e2e-cluster-2):
        each watches mongodbsearch only, identity = OPERATOR_CLUSTER_NAME env var,
        per-cluster LocalizeToCluster collapse selects spec.clusters[i] slice.

Each member's API server receives THE SAME MongoDBSearch CR (spec.clusters lists
every cluster, spec.source.external.shardedCluster is set with router hosts and
per-shard hosts on the central sharded MongoDB). The simulated operator on each
member projects to its own slice and materialises per-(cluster, shard) mongot
StatefulSets / Services / ConfigMaps / proxy Services + a per-cluster Envoy LB.

Data-plane testing ($search, mongorestore, search index lifecycle) is OUT OF SCOPE
for this test. The kind e2e topology installs Istio only between members 1/2/3 and
runs `install_istio_central.sh` without remote-secret wiring on `kind-e2e-operator`,
so the central sharded mongod/mongos cannot resolve member-cluster Envoy FQDNs and
member mongot pods cannot resolve the central source. Same scaffold-only assertion
boundary as `simulated_mc_basic.py` and the q2/q3 MC search tests.
"""

from __future__ import annotations

import os
from typing import Dict, List, Tuple

import kubernetes
from kubernetes.client import CoreV1Api
from kubetester import create_or_update_configmap, create_or_update_secret, read_secret, try_load
from kubetester.certs import create_sharded_cluster_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.sharded_search_helper import (
    create_issuer_ca,
    create_lb_certificates,
    create_per_shard_search_tls_certs,
)

logger = test_logger.get_test_logger(__name__)

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

MDB_RESOURCE_NAME = "mdb-mc-sim-sh"
MDBS_RESOURCE_NAME = "mdb-mc-sim-sh-search"

# Single-cluster sharded source — see comment block above for why we don't use MC.
SHARD_COUNT = 2
MONGODS_PER_SHARD = 1
MONGOS_COUNT = 1
CONFIG_SERVER_COUNT = 1

MONGOT_REPLICAS_PER_CLUSTER = 1
ENVOY_LB_REPLICAS = 1
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
# per-component secret name as f"{prefix}-{resourceName}-{shardIdx}-cert" (note
# the literal dash). create_sharded_cluster_certs(secret_prefix=...) does NOT
# inject that dash itself, so we pass `f"{SOURCE_CERT_PREFIX}-"` at the helper
# call site and store the bare prefix on the CR.
SOURCE_CERT_PREFIX = "clustercert"


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
def ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    """Write the issuer CA as both ConfigMap (ca-pem/mms-ca.crt) and Secret (ca.crt) on central."""
    return create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)


@fixture(scope="module")
def mdb(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    ca_configmap: str,
) -> MongoDB:
    """Single-cluster sharded MongoDB with TLS+SCRAM, applied to the central cluster only.

    The data-plane mongotHost / searchIndexManagementHostAndPort are intentionally
    omitted — this is a scaffold-only test (see module docstring), so we don't need
    mongos/mongod to route to per-cluster Envoy proxies. Setting only the non-routing
    search params (`searchTLSMode`, `useGrpcForSearch`, skip-auth flags) keeps the
    source consistent with the other simulated-MC tests' search-enabled mongod config.
    """
    resource = MongoDB.from_yaml(
        yaml_fixture("simulated-mc-sharded-mongodb.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )

    base_search_set_parameter = {
        "skipAuthenticationToSearchIndexManagementServer": False,
        "skipAuthenticationToMongot": False,
        "searchTLSMode": "requireTLS",
        "useGrpcForSearch": True,
    }
    resource["spec"]["shard"]["additionalMongodConfig"] = {"setParameter": base_search_set_parameter}
    resource["spec"]["mongos"]["additionalMongodConfig"] = {"setParameter": base_search_set_parameter}

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

    # Single-cluster sharded MongoDB pod/service hostnames on central:
    # mongos: {name}-mongos-{i}.{name}-svc.{ns}.svc.cluster.local:27017
    # shard:  {name}-{shardIdx}-{m}.{name}-sh.{ns}.svc.cluster.local:27017
    router_hosts = [
        f"{MDB_RESOURCE_NAME}-mongos-{i}.{MDB_RESOURCE_NAME}-svc.{namespace}.svc.cluster.local:27017"
        for i in range(MONGOS_COUNT)
    ]
    shards = [
        {
            "shardName": f"{MDB_RESOURCE_NAME}-{shard_idx}",
            "hosts": [
                f"{MDB_RESOURCE_NAME}-{shard_idx}-{m}.{MDB_RESOURCE_NAME}-sh.{namespace}.svc.cluster.local:27017"
                for m in range(MONGODS_PER_SHARD)
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
    mdb: MongoDB,
):
    """Source sharded MongoDB TLS certs — single-cluster on central only.

    `create_sharded_cluster_certs` issues one secret per component with the prefix:
    {prefix}-{name}-{shardIdx}-cert, {prefix}-{name}-config-cert, {prefix}-{name}-mongos-cert.
    SANs cover central-local pod and Service FQDNs; no cross-cluster SANs needed since
    the source data plane is not exercised from member clusters in this scaffold test.
    """
    mongos_service_dns = f"{MDB_RESOURCE_NAME}-svc.{namespace}.svc.cluster.local"
    create_sharded_cluster_certs(
        namespace=namespace,
        resource_name=MDB_RESOURCE_NAME,
        shards=SHARD_COUNT,
        mongod_per_shard=MONGODS_PER_SHARD,
        config_servers=CONFIG_SERVER_COUNT,
        mongos=MONGOS_COUNT,
        secret_prefix=f"{SOURCE_CERT_PREFIX}-",
        mongos_service_dns_names=[mongos_service_dns],
    )
    logger.info(f"Source sharded cluster TLS certs created on central (prefix={SOURCE_CERT_PREFIX!r})")


@mark.e2e_search_simulated_mc_sharded_basic
def test_mongodb_running(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=1500)


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
        # CA stored as both ConfigMap and Secret (create_issuer_ca writes both).
        # Simulated operator's source.external.tls.ca reference resolves to the
        # Secret half (key: ca.crt) — must be on every member.
        CA_CONFIGMAP_NAME,
    ]
    for secret_name in shared_secret_names:
        secret_type = central_core.read_namespaced_secret(name=secret_name, namespace=namespace).type or "Opaque"
        data = read_secret(namespace, secret_name, api_client=central_cluster_client)
        for mcc in member_cluster_clients:
            create_or_update_secret(namespace, secret_name, data, type=secret_type, api_client=mcc.api_client)
        logger.info(f"replicated Secret {secret_name} to {len(member_cluster_clients)} member cluster(s)")

    # CA ConfigMap half (key: ca-pem / mms-ca.crt) for future code paths reading CA from ConfigMap.
    source_cm = central_core.read_namespaced_config_map(name=CA_CONFIGMAP_NAME, namespace=namespace)
    cm_data = dict(source_cm.data or {})
    for mcc in member_cluster_clients:
        create_or_update_configmap(namespace, CA_CONFIGMAP_NAME, cm_data, api_client=mcc.api_client)
        logger.info(f"replicated CA ConfigMap {CA_CONFIGMAP_NAME} into cluster {mcc.cluster_name}")

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
# Tests
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
    """Final assertion that the central sharded MongoDB source remained Running through
    setup. Distinct from `test_mongodb_running` (which drives the initial apply): a
    flake here means the source flipped phases during the later TLS/user/replication
    steps and would invalidate downstream Search assertions.
    """
    mdb.load()
    actual = mdb.get_status_phase()
    assert actual == Phase.Running, f"central sharded MongoDB {mdb.name} phase={actual}, expected Phase.Running"


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
