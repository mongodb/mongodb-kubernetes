"""Shared fixtures and helpers for the MC-Search e2e tests.

Mirrors `tests/search/conftest.py` for the tools_pod fixture so the
data-plane tests have a pod for `mongorestore` runs. Other shared
fixtures (namespace, central_cluster_client, member_cluster_clients,
member_cluster_names, multi_cluster_operator) come from
`tests/conftest.py` and `tests/multicluster/conftest.py`.

Also exposes simulated-MC helpers/constants shared between the
RS (`simulated_mc_basic.py`) and sharded (`simulated_mc_sharded_basic.py`)
variants. Constants that differ per variant (e.g., MDB_RESOURCE_NAME,
MEMBERS_PER_CLUSTER, MONGOT_REPLICAS_PER_CLUSTER) stay in the test
modules. Helpers that close over per-variant constants take them as
parameters here.
"""

from __future__ import annotations

import json
import os
from typing import Callable, Dict, List

import kubernetes
import yaml
from kubernetes.client.rest import ApiException
from kubetester import create_or_update_secret, read_secret, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from pytest import fixture
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import search_resource_names
from tests.common.search.search_tester import SearchTester
from tests.conftest import get_issuer_ca_filepath

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
def tools_pod(namespace: str) -> mongodb_tools_pod.ToolsPod:
    return mongodb_tools_pod.get_tools_pod(namespace)


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

    # .upgrade(multi_cluster=True) = `helm upgrade --install` semantics + skip
    # the validating-webhook wait. The simulated operator only watches
    # MongoDBSearch (no validating webhook for that CRD); the test never
    # applies a MongoDB CR against this operator. The test pod is in
    # kind-e2e-cluster-1 which != central_cluster_name (kind-e2e-operator),
    # so the multi_cluster=True branch at kubetester/operator.py:192 skips
    # the wait.
    operator = Operator(
        namespace=namespace,
        name=SIMULATED_OPERATOR_NAME,  # matches helm_args["operator.name"] for pod-label polling
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
    mdbs_resource_name: str,
) -> None:
    """Replicate the password Secret so simulated-MC operators' source.passwordSecretRef resolves locally."""
    # Kind e2e variant exposes 3 member clusters; this test exercises a 2-cluster
    # simulated-MC topology, so restrict to the first two members.
    member_cluster_clients = member_cluster_clients[:2]
    pwd_secret_name = f"{mdbs_resource_name}-{MONGOT_USER_NAME}-password"
    src = read_secret(namespace, pwd_secret_name, api_client=central_cluster_client)
    for mcc in member_cluster_clients:
        create_or_update_secret(namespace, pwd_secret_name, src, api_client=mcc.api_client)
        logger.info(f"replicated {pwd_secret_name} to {mcc.cluster_name}")


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
# Comprehensive happy-path helpers (shared by RS + sharded variants)
# ---------------------------------------------------------------------------


def assert_per_cluster_count(per_cluster: List, expected: int = 2) -> None:
    """Vacuous-green guard: every per-cluster loop test must iterate `expected`
    clusters. Without this a fixture that silently yields 0 entries makes the
    loop body never run, so the test passes while asserting nothing.
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
    """Assert THIS cluster's client cannot see the OTHER cluster's index-suffixed
    Search resources — proves the per-operator LocalizeToCluster projection only
    materialises its own slice (no cross-cluster fan-out leak).

    Every read must raise 404 (IsNotFound). A read that SUCCEEDS fails the test;
    any non-404 ApiException is re-raised (don't mask RBAC/connectivity errors as
    "absent").
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
    else:
        sts = f"{mdbs_name}-search-{foreign_idx}"
        svc = f"{mdbs_name}-search-{foreign_idx}-svc"
        proxy = f"{mdbs_name}-search-{foreign_idx}-proxy-svc"
        cm = f"{mdbs_name}-search-{foreign_idx}-config"
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


def read_mongod_set_parameter(
    pod_name: str,
    namespace: str,
    api_client: kubernetes.client.ApiClient,
) -> Dict[str, object]:
    """Read /data/automation-mongod.conf inside a mongod pod and return the parsed
    `setParameter` map. Caller chooses the api_client (member cluster). Generalized
    from q2_mc_rs_steady._read_mongod_set_parameter — only valid for mongod pods
    (mongos is stateless and has no automation-mongod.conf; use getParameter there).
    """
    raw = KubernetesTester.run_command_in_pod_container(
        pod_name,
        namespace,
        ["cat", "/data/automation-mongod.conf"],
        api_client=api_client,
    )
    parsed = yaml.safe_load(raw) or {}
    return parsed.get("setParameter", {}) or {}


def assert_mongot_host_on_disk(
    mdb,
    expected_by_idx: Dict[int, str],
    member_clients: List[MultiClusterClient],
    *,
    pod_name_for_idx: Callable[[object, int], str] | None = None,
    timeout: int = 300,
) -> None:
    """Poll each cluster's first mongod pod and confirm both `mongotHost` and
    `searchIndexManagementHostAndPort` resolve to `expected_by_idx[cluster_index]`.

    Reads the on-disk /data/automation-mongod.conf so this verifies the AC patch
    landed AND was applied by the agent. `pod_name_for_idx(mdb, idx)` selects the
    pod to inspect (defaults to the RS first-member name `{mdb.name}-{idx}-0`).
    `expected_by_idx` is an explicit cluster_index -> endpoint map so callers can
    encode either per-cluster locality (RS) or single-path collapse (sharded).
    """
    if pod_name_for_idx is None:

        def pod_name_for_idx(m, idx):
            return f"{m.name}-{idx}-0"

    def check() -> tuple:
        all_correct = True
        msgs: List[str] = []
        for mcc in member_clients:
            cluster_idx = _idx(mcc)
            pod_name = pod_name_for_idx(mdb, cluster_idx)
            expected = expected_by_idx[cluster_idx]
            try:
                params = read_mongod_set_parameter(pod_name, mdb.namespace, mcc.api_client)
                got_host = params.get("mongotHost", "")
                got_idx_mgmt = params.get("searchIndexManagementHostAndPort", "")
                if got_host != expected:
                    all_correct = False
                    msgs.append(f"[{mcc.cluster_name}] {pod_name}: mongotHost={got_host!r} expected={expected!r}")
                elif got_idx_mgmt != expected:
                    all_correct = False
                    msgs.append(
                        f"[{mcc.cluster_name}] {pod_name}: searchIndexManagementHostAndPort="
                        f"{got_idx_mgmt!r} expected={expected!r}"
                    )
                else:
                    msgs.append(f"[{mcc.cluster_name}] {pod_name}: mongotHost={expected} OK")
            except Exception as exc:
                all_correct = False
                msgs.append(f"[{mcc.cluster_name}] {pod_name}: error reading conf: {exc}")
        return all_correct, "\n".join(msgs)

    run_periodically(check, timeout=timeout, sleep_time=10, msg="per-cluster mongotHost on disk")


def per_cluster_search_tester(
    host: str,
    username: str,
    password: str,
    *,
    direct: bool = True,
) -> SearchTester:
    """SearchTester pinned to a single cluster's mongod/mongos `host` (`name:port`).

    `direct=True` sets directConnection=true so RS topology discovery does not
    silently follow back to the primary — the connected pod serves the query.
    `$search` is a read aggregation any node can serve, so readPreference=
    secondaryPreferred lets a secondary answer. TLS + the issuer CA are always on
    (the source runs requireTLS).
    """
    opts = "directConnection=true&readPreference=secondaryPreferred&" if direct else ""
    conn_str = f"mongodb://{username}:{password}@{host}/?{opts}authSource=admin"
    return SearchTester(conn_str, use_ssl=True, ca_path=get_issuer_ca_filepath())


def read_state_configmap_mapping(
    mcc: MultiClusterClient,
    mdbs_name: str,
    namespace: str,
) -> Dict[str, int]:
    """Read the `{mdbs_name}-state` ConfigMap on `mcc`'s cluster and return its
    `clusterMapping` dict. In simulated-MC mode each operator narrows spec.clusters[]
    to its own slice (LocalizeToCluster) before persisting, so this map is LOCAL —
    it contains only this cluster's entry, written to this member cluster's etcd.
    """
    cm = mcc.read_namespaced_config_map(f"{mdbs_name}-state", namespace)
    raw = (cm.data or {}).get("state")
    assert raw, (
        f"[{mcc.cluster_name}] state ConfigMap {mdbs_name}-state missing 'state' key; "
        f"got keys {list((cm.data or {}).keys())}"
    )
    state = json.loads(raw)
    return state.get("clusterMapping") or {}
