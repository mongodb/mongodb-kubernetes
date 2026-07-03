"""Shared fixtures and helpers for the MC-Search e2e tests.

Mirrors `tests/search/conftest.py` for the tools_pod fixture so the
data-plane tests have a pod for `mongorestore` runs. Other shared
fixtures (namespace, central_cluster_client, member_cluster_clients,
member_cluster_names, multi_cluster_operator) come from
`tests/conftest.py` and `tests/multicluster/conftest.py`.

Also exposes simulated-MC helpers/constants shared between the
RS (`simulated_mc_rs.py`) and sharded (`simulated_mc_sharded.py`)
variants.
"""

from __future__ import annotations

import json
import os
from typing import Callable, Dict, List, Optional, Tuple

import kubernetes
from kubernetes.client.rest import ApiException
from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import search_resource_names
from tests.conftest import get_multi_cluster_operator

logger = test_logger.get_test_logger(__name__)

# ---------------------------------------------------------------------------
# Shared simulated-MC constants
# ---------------------------------------------------------------------------

ENVOY_LB_REPLICAS = 1
ENVOY_PROXY_PORT = 27028
SIMULATED_OPERATOR_NAME = "mongodb-kubernetes-operator-simulated"

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"

USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"

# Mongot/LB server+client cert prefix on the MongoDBSearch CR; source MongoDB cert prefix.
MDBS_TLS_CERT_PREFIX = "certs"
SOURCE_CERT_PREFIX = "clustercert"


@fixture(scope="module")
def tools_pod(namespace: str, member_cluster_clients: List[MultiClusterClient]) -> mongodb_tools_pod.ToolsPod:
    # Run the tools pod in a mesh-joined member cluster so it can resolve the
    # cross-cluster RS/mongos member-service DNS. The central operator cluster has
    # no istio sidecar, so a tools pod there gets "no such host" on member services.
    return mongodb_tools_pod.get_tools_pod(namespace, api_client=member_cluster_clients[0].api_client)


# ---------------------------------------------------------------------------
# Shared simulated-MC helpers
# ---------------------------------------------------------------------------


def _idx(mcc: MultiClusterClient) -> int:
    assert mcc.cluster_index is not None, f"cluster_index unset on {mcc.cluster_name!r}"
    return mcc.cluster_index


def _install_simulated_operator(
    namespace: str,
    base_helm_args: Dict[str, str],
    mcc: MultiClusterClient,
) -> Operator:
    """Uses a DISTINCT operator.name so Helm renders fresh SA+Role+RoleBinding with
    mongodbsearch RBAC (the kube-config-creation-tool SA's Role lacks search perms).

    Serving the full CRD set (incl. MongoDB) is GC-safe: search clusters hold no
    MongoDB-owned objects, and the sharded source's cross-cluster ownerRef can't arm GC here.
    """
    os.environ["HELM_KUBECONTEXT"] = mcc.cluster_name

    helm_args = dict(base_helm_args)
    # multiCluster.clusters gates a kube-config-volume mount whose Secret is never created in this mode.
    for k in (
        "operator.watchNamespace",
        "multiCluster.clusters",
        "multiCluster.kubeConfigSecretName",
        "multiCluster.performFailover",
    ):
        helm_args.pop(k, None)

    helm_args["operator.name"] = SIMULATED_OPERATOR_NAME
    helm_args["operator.createOperatorServiceAccount"] = "true"
    # MongoDB-resource-pod SAs are pre-created by run_kube_config_creation_tool — Helm 3
    # ownership-metadata check fails if we re-render them.
    helm_args["operator.createResourcesServiceAccountsAndRoles"] = "false"
    helm_args["operator.clusterIdentity.clusterName"] = mcc.cluster_name

    # upgrade(multi_cluster=True) = `helm upgrade --install`, and skips the
    # webhook wait because the test pod's cluster != central cluster, so it can't
    # reach the webhook endpoint (the multi_cluster branch at kubetester/operator.py:192).
    operator = Operator(
        namespace=namespace,
        name=SIMULATED_OPERATOR_NAME,
        helm_args=helm_args,
        api_client=mcc.api_client,
    ).upgrade(multi_cluster=True)
    # Restrict this operator to mongodbsearch only via OperatorConfig (the watch set is no longer a
    # Helm value). This triggers a graceful restart so the operator reloads and watches search only.
    operator.apply_operator_config_and_wait(multi_cluster=True, extra_spec={"watchedResources": ["mongodbsearch"]})
    logger.info(
        f"installed simulated-MC operator in {mcc.cluster_name} "
        f"(identity={mcc.cluster_name}, name={SIMULATED_OPERATOR_NAME}, watches=mongodbsearch only)"
    )
    return operator


def install_central_mc_operator(
    namespace: str,
    central_cluster_name: str,
    install_config: dict,
    central_client: kubernetes.client.ApiClient,
    member_clients: List[MultiClusterClient],
    member_names: List[str],
    *,
    watch_search: bool,
) -> Operator:
    """Central MC operator for the source MongoDB.

    watch_search=True registers the MongoDBSearch field index the sharded controller's
    lookupCorrespondingSearchResource needs every reconcile; the central op never
    reconciles member-cluster search CRs (they live in member etcd, so it bails on
    NotFound). The RS variant omits mongodbsearch entirely.
    """
    watched = ["mongodb", "opsmanagers", "mongodbusers", "mongodbcommunity"]
    if watch_search:
        watched.append("mongodbsearch")
    watched.append("mongodbmulticluster")

    return get_multi_cluster_operator(
        namespace,
        central_cluster_name,
        install_config,
        central_client,
        member_clients,
        member_names,
        operator_config_extra_spec={"watchedResources": watched},
    )


def _build_user(
    yaml_filename: str,
    name: str,
    username: str,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    mdb_resource_name: str,
) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(yaml_fixture(yaml_filename), namespace=namespace, name=name)
    if not try_load(resource):
        resource["spec"]["mongodbResourceRef"]["name"] = mdb_resource_name
        resource["spec"]["username"] = username
        resource["spec"]["passwordSecretKeyRef"]["name"] = f"{name}-password"
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource


# ---------------------------------------------------------------------------
# Shared simulated-MC fixtures (RS + sharded variants only). They resolve the
# per-variant resource names via `request.module`; inert for q2/q3 (those
# modules define their own user fixtures or never request these).
# ---------------------------------------------------------------------------


@fixture(scope="module")
def admin_user(namespace: str, central_cluster_client: kubernetes.client.ApiClient, request) -> MongoDBUser:
    return _build_user(
        "mongodbuser-mdb-admin.yaml",
        ADMIN_USER_NAME,
        ADMIN_USER_NAME,
        namespace,
        central_cluster_client,
        request.module.MDB_RESOURCE_NAME,
    )


@fixture(scope="module")
def mdb_user(namespace: str, central_cluster_client: kubernetes.client.ApiClient, request) -> MongoDBUser:
    return _build_user(
        "mongodbuser-mdb-user.yaml",
        USER_NAME,
        USER_NAME,
        namespace,
        central_cluster_client,
        request.module.MDB_RESOURCE_NAME,
    )


@fixture(scope="module")
def mongot_user(namespace: str, central_cluster_client: kubernetes.client.ApiClient, request) -> MongoDBUser:
    return _build_user(
        "mongodbuser-search-sync-source-user.yaml",
        f"{request.module.MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}",
        MONGOT_USER_NAME,
        namespace,
        central_cluster_client,
        request.module.MDB_RESOURCE_NAME,
    )


# ---------------------------------------------------------------------------
# Shared simulated-MC test bodies (per-file test_* wrappers keep the test ids)
# ---------------------------------------------------------------------------


def install_simulated_operators_per_member(
    namespace: str,
    multi_cluster_operator_installation_config: dict,
    member_cluster_clients: List[MultiClusterClient],
) -> None:
    member_cluster_clients = member_cluster_clients[:2]
    base_helm_args = dict(multi_cluster_operator_installation_config)
    for mcc in member_cluster_clients:
        operator = _install_simulated_operator(namespace, base_helm_args, mcc)
        operator.wait_for_operator_ready()


def create_search_users(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    admin_user: MongoDBUser,
    mdb_user: MongoDBUser,
    mongot_user: MongoDBUser,
) -> None:
    # Member-cluster replication of the mongot password ships with the other search
    # secrets (replicate_*_search_secrets_to_members).
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


def build_per_cluster_search_crs(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
    mdbs_resource_name: str,
    mongot_replicas: int,
    external_hostname_for_cluster: Callable[[int], str],
    source_external: dict,
    router_hostname_for_cluster: Optional[Callable[[int], str]] = None,
) -> List[Tuple[MultiClusterClient, MongoDBSearch]]:
    """Build the SAME MongoDBSearch CR (spec.clusters lists all clusters) against each
    member's API; each simulated operator projects to its own slice. `source_external`
    carries the variant-specific spec.source.external block (hostAndPorts vs shardedCluster).

    `external_hostname_for_cluster(cluster_index)` returns that cluster's literal managed-LB
    externalHostname; #1238 moved the LB under spec.clusters[] and dropped the per-cluster
    hostname placeholder, so each cluster needs a distinct literal (validation rejects duplicates).

    `router_hostname_for_cluster(cluster_index)`, when provided (external sharded sources only),
    returns that cluster's shard-agnostic routerHostname (the mongos-facing cluster-level endpoint).
    Required and validated for external sharded + managed LB; omitted for ReplicaSet sources.
    """
    results: List[Tuple[MultiClusterClient, MongoDBSearch]] = []

    def _managed(idx: int) -> dict:
        managed = {
            "replicas": ENVOY_LB_REPLICAS,
            "externalHostname": external_hostname_for_cluster(idx),
        }
        if router_hostname_for_cluster is not None:
            managed["routerHostname"] = router_hostname_for_cluster(idx)
        return managed

    clusters_spec = [
        {
            "name": mcc.cluster_name,
            "index": _idx(mcc),
            "replicas": mongot_replicas,
            "loadBalancer": {"managed": _managed(_idx(mcc))},
        }
        for mcc in member_cluster_clients
    ]

    for mcc in member_cluster_clients:
        mdbs = MongoDBSearch.from_yaml(
            yaml_fixture("simulated-mc-search.yaml"),
            name=mdbs_resource_name,
            namespace=namespace,
        )
        mdbs["spec"]["clusters"] = clusters_spec
        mdbs["spec"]["source"] = {
            "username": MONGOT_USER_NAME,
            "passwordSecretRef": {
                "name": f"{mdbs_resource_name}-{MONGOT_USER_NAME}-password",
                "key": "password",
            },
            "external": source_external,
        }
        mdbs["spec"]["security"] = {
            "tls": {"certsSecretPrefix": MDBS_TLS_CERT_PREFIX},
        }
        mdbs.api = kubernetes.client.CustomObjectsApi(mcc.api_client)
        try_load(mdbs)
        results.append((mcc, mdbs))

    return results


def apply_search_crs_and_assert_running(
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
) -> None:
    assert_per_cluster_count(per_cluster_mdbs_search)
    for _mcc, mdbs in per_cluster_mdbs_search:
        mdbs.update()
    for mcc, mdbs in per_cluster_mdbs_search:
        mdbs.assert_reaches_phase(Phase.Running, timeout=1200)
        logger.info(f"MongoDBSearch {mdbs.name} Running in {mcc.cluster_name}")


def assert_status_running_local_only(
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
) -> None:
    assert_per_cluster_count(per_cluster_mdbs_search)
    names = {mcc.cluster_name for mcc, _ in per_cluster_mdbs_search}
    for mcc, mdbs in per_cluster_mdbs_search:
        mdbs.load()
        phase = mdbs.get_status_phase()
        assert phase == Phase.Running, f"[{mcc.cluster_name}] MongoDBSearch {mdbs.name} phase={phase}, expected Running"
        assert_status_no_foreign_cluster(mdbs, names - {mcc.cluster_name})


def assert_cross_cluster_isolation(
    namespace: str,
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
    mdbs_resource_name: str,
    shard_names: List[str] | None = None,
) -> None:
    """Assert each cluster materialised its OWN slice (anti-vacuous anchor) and does NOT
    contain the other cluster's index-suffixed Search resources (LocalizeToCluster isolation).
    """
    assert_per_cluster_count(per_cluster_mdbs_search)
    indices = [_idx(mcc) for mcc, _ in per_cluster_mdbs_search]
    for mcc, _mdbs in per_cluster_mdbs_search:
        this_idx = _idx(mcc)
        assert_local_slice_present(mcc, mdbs_resource_name, this_idx, namespace)
        for foreign_idx in indices:
            if foreign_idx == this_idx:
                continue
            assert_foreign_resources_absent(
                mcc,
                mdbs_resource_name,
                foreign_idx,
                namespace,
                sharded=bool(shard_names),
                shard_names=shard_names,
            )


# ---------------------------------------------------------------------------
# Comprehensive happy-path helpers (shared by RS + sharded variants)
# ---------------------------------------------------------------------------


def assert_per_cluster_count(per_cluster: List, expected: int = 2) -> None:
    """Vacuous-green guard: a fixture that silently yields 0 entries would make every
    per-cluster loop body never run, so the test passes while asserting nothing.
    """
    assert len(per_cluster) == expected, (
        f"expected {expected} per-cluster entries to iterate, got {len(per_cluster)}; "
        f"a per-cluster loop test over fewer clusters would be vacuously green"
    )


def assert_foreign_resources_absent(
    mcc: MultiClusterClient,
    mdbs_name: str,
    foreign_idx: int,
    namespace: str,
    *,
    sharded: bool = False,
    shard_names: List[str] | None = None,
) -> None:
    """Every read must raise 404 (IsNotFound); a read that SUCCEEDS fails the test, and
    any non-404 ApiException is re-raised so RBAC/connectivity errors aren't masked as
    "absent". Proves the per-operator LocalizeToCluster projection only materialises its
    own slice.
    """

    def _assert_404(read_fn: Callable[[], object], what: str) -> None:
        try:
            read_fn()
        except ApiException as exc:
            if exc.status == 404:
                return
            raise
        raise AssertionError(
            f"[{mcc.cluster_name}] foreign resource {what} (idx={foreign_idx}) is present "
            f"but must NOT exist on this cluster — LocalizeToCluster must only materialise the local slice"
        )

    apps = mcc.apps_v1_api()
    if sharded:
        assert shard_names, "sharded=True requires shard_names"
        for shard_name in shard_names:
            sts = search_resource_names.shard_statefulset_name(mdbs_name, shard_name, foreign_idx)
            svc = search_resource_names.shard_service_name(mdbs_name, shard_name, foreign_idx)
            proxy = search_resource_names.shard_proxy_service_name(mdbs_name, shard_name, foreign_idx)
            cm = search_resource_names.shard_configmap_name(mdbs_name, shard_name, foreign_idx)
            _assert_404(lambda n=sts: mcc.read_namespaced_stateful_set(n, namespace), f"STS {sts}")
            _assert_404(lambda n=svc: mcc.read_namespaced_service(n, namespace), f"Service {svc}")
            _assert_404(lambda n=proxy: mcc.read_namespaced_service(n, namespace), f"proxy Service {proxy}")
            _assert_404(lambda n=cm: mcc.read_namespaced_config_map(n, namespace), f"mongot CM {cm}")
        # Cluster-level mongos-target proxy svc is per-cluster (not per-shard) — absent once.
        cluster_proxy = search_resource_names.mc_proxy_svc_name(mdbs_name, foreign_idx)
        _assert_404(
            lambda n=cluster_proxy: mcc.read_namespaced_service(n, namespace),
            f"cluster-level proxy Service {cluster_proxy}",
        )
    else:
        sts = search_resource_names.mongot_statefulset_name_for_cluster(mdbs_name, foreign_idx)
        svc = search_resource_names.mongot_service_name_for_cluster(mdbs_name, foreign_idx)
        proxy = search_resource_names.mc_proxy_svc_name(mdbs_name, foreign_idx)
        cm = search_resource_names.mongot_configmap_name_for_cluster(mdbs_name, foreign_idx)
        _assert_404(lambda: mcc.read_namespaced_stateful_set(sts, namespace), f"STS {sts}")
        _assert_404(lambda: mcc.read_namespaced_service(svc, namespace), f"Service {svc}")
        _assert_404(lambda: mcc.read_namespaced_service(proxy, namespace), f"proxy Service {proxy}")
        _assert_404(lambda: mcc.read_namespaced_config_map(cm, namespace), f"mongot CM {cm}")

    foreign_envoy = search_resource_names.lb_deployment_name(mdbs_name, foreign_idx)
    _assert_404(
        lambda: apps.read_namespaced_deployment(name=foreign_envoy, namespace=namespace),
        f"Envoy Deployment {foreign_envoy}",
    )
    logger.info(f"[{mcc.cluster_name}] confirmed foreign idx={foreign_idx} Search resources absent")


def assert_local_slice_present(mcc: MultiClusterClient, mdbs_name: str, local_idx: int, namespace: str) -> None:
    """Positive co-anchor: assert THIS cluster materialised its own Envoy slice, so the
    foreign-absent (all-404) checks can't pass vacuously against an operator that created nothing.
    """
    local_envoy = search_resource_names.lb_deployment_name(mdbs_name, local_idx)
    try:
        mcc.apps_v1_api().read_namespaced_deployment(name=local_envoy, namespace=namespace)
    except ApiException as exc:
        raise AssertionError(
            f"[{mcc.cluster_name}] local Envoy Deployment {local_envoy} (idx={local_idx}) is ABSENT "
            f"(status={exc.status}); the local slice must exist before asserting foreign slices absent"
        )
    logger.info(f"[{mcc.cluster_name}] local Envoy Deployment {local_envoy} (idx={local_idx}) present")


def assert_status_no_foreign_cluster(mdbs, foreign_names) -> None:
    """Scoped to `status`, NOT the whole CR: spec.clusters[] still lists every cluster
    (LocalizeToCluster narrows in-memory per reconcile, not in etcd). Status carries no
    per-cluster enumeration, so a foreign name there is the strongest available leak signal.
    """
    assert (
        foreign_names
    ), "assert_status_no_foreign_cluster called with no foreign cluster names — vacuously green otherwise"
    status_blob = json.dumps(mdbs["status"])
    for other in foreign_names:
        assert other not in status_blob, (
            f"[{mdbs.name}] foreign cluster {other!r} leaked into status: {status_blob} — "
            f"simulated-MC status must carry no cross-cluster awareness"
        )
    logger.info(
        f"[{mdbs.name}] status phase={(mdbs['status'] or {}).get('phase')}; "
        f"no foreign cluster name in status (verified absent: {sorted(foreign_names)})"
    )
