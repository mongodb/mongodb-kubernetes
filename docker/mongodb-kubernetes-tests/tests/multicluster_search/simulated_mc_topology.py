"""Topology-changing e2e for simulated multi-cluster MongoDBSearch.

Topology: 1 central operator (source MongoDB only) + simulated-MC search operators
on member clusters 1/2; the RS source (MongoDBMulti, single member) is PINNED to
member cluster 3, which initially runs NO search operator. Coverage, in order:

(a) permuted, non-positional index pins: spec.clusters[] = [cluster-2 pin 5,
    cluster-1 pin 2] — per-cluster resources must be named by the PIN, never the
    array position, in the correct clusters;
(a2) data plane: $search SERVES end-to-end through BOTH permuted pins. The single
    source RS has one mongotHost, so it is FLIPPED between pin-2's and pin-5's Envoy
    proxy (the RS analogue of simulated_mc_sharded's flip); each flip is verified
    APPLIED on disk, then a deterministic $search (==4) is asserted via the source;
(b) admission negatives (CEL/schema on the CRD — unpinned or partially pinned
    multi-cluster specs never reach the operator) + the operator-level rejection of
    an unpinned SINGLE-entry spec (which passes admission) surfaced via status by
    the local simulated operator;
(c) unlisted-operator no-op: a third simulated operator on the source cluster
    (whose name is NOT in spec.clusters[]) must create nothing and write no status;
(d) remove/re-add lifecycle asserting the CURRENT v0 contract: removal orphans the
    removed cluster's resources in place (owner labels retained for a future sweep),
    re-add with the same pin reconverges under the same names.

Scoped data plane: one text $search per pin via the mongotHost flip above (no
$vectorSearch — avoids the Voyage key dep; no per-cluster mongod load — the source
is a single RS). Broader data-plane coverage lives in simulated_mc_rs.py /
simulated_mc_sharded.py.

spec.clusters[].index is a pure-CRD pin (there is no persisted index mapping), so
per-cluster resource names derive directly from the pin; there is no mapping state to
assert, reconcile, or evict.
"""

from __future__ import annotations

import copy
from typing import Dict, List, Tuple

import kubernetes
from kubernetes.client.rest import ApiException
from kubetester import try_load
from kubetester.certs import create_tls_certs
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod.mongodb_tools_pod import ToolsPod
from tests.common.multicluster.multicluster_utils import assert_workload_ready_in_cluster
from tests.common.search import search_resource_names
from tests.common.search.bootstrap_test_mixins import SEARCH_SET_PARAMETERS
from tests.common.search.mc_search_helper import (
    assert_mongot_sync_source_hosts,
    patch_mongot_host_via_ac,
    read_mongod_set_parameter,
    replicate_search_secrets_to_members,
    verify_per_cluster_envoy_sni,
)
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_tester import SearchTester
from tests.common.search.sharded_search_helper import create_issuer_ca
from tests.conftest import get_issuer_ca_filepath
from tests.multicluster.conftest import cluster_spec_list
from tests.multicluster_search.conftest import (
    ADMIN_USER_NAME,
    ADMIN_USER_PASSWORD,
    ENVOY_LB_REPLICAS,
    ENVOY_PROXY_PORT,
    MDBS_TLS_CERT_PREFIX,
    MONGOT_USER_NAME,
    SIMULATED_OPERATOR_NAME,
    SOURCE_CERT_PREFIX,
    USER_NAME,
    USER_PASSWORD,
    _idx,
    _install_simulated_operator,
    apply_search_crs_and_assert_running,
    assert_foreign_resources_absent,
    assert_per_cluster_count,
    assert_status_running_local_only,
    build_per_cluster_search_crs,
    create_search_users,
    install_central_mc_operator,
    install_simulated_operators_per_member,
)

logger = test_logger.get_test_logger(__name__)

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

MDB_RESOURCE_NAME = "mdb-mc-topo"
MDBS_RESOURCE_NAME = "mdb-mc-topo-search"

# Source RS pinned to member cluster 3 (harness member index 2): it hosts the
# unlisted-operator test (c) and keeps clusters 1/2 dedicated to search. One
# referenced cluster => source-internal cluster index 0 (pods {mdb}-0-{n}),
# regardless of the harness index of the cluster it lands on.
SOURCE_MEMBER_INDEX = 2
SOURCE_CLUSTER_IDX = 0
SOURCE_MEMBERS = 1

MONGOT_REPLICAS_PER_CLUSTER = 1

# Permuted pins: harness member index -> pinned index. Neither pin matches
# its spec.clusters[] array position (array order is [cluster-2 pin 5, cluster-1
# pin 2]), so positional naming regressions cannot hide.
PIN_BY_MEMBER_INDEX: Dict[int, int] = {0: 2, 1: 5}
# Array positions a positional-naming regression would name resources with.
POSITIONAL_INDICES = (0, 1)

CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"
SOURCE_BUNDLE_SECRET = f"{SOURCE_CERT_PREFIX}-{MDB_RESOURCE_NAME}-cert"

SEARCH_OWNER_NAME_LABEL = "mongodb.com/search-name"
SEARCH_OWNER_NAMESPACE_LABEL = "mongodb.com/search-namespace"
SEARCH_CLUSTER_NAME_LABEL = "mongodb.com/cluster-name"

# Logged by prepareSearchForReconcile when spec.clusters[] does not list the
# operator's cluster — the positive anchor that an operator OBSERVED a CR it
# does not own (so the absence asserts that follow cannot pass vacuously).
LOCALIZE_SKIP_LOG_SNIPPET = "spec.clusters does not list this operator's cluster"


def _pin(mcc: MultiClusterClient) -> int:
    return PIN_BY_MEMBER_INDEX[_idx(mcc)]


def _pins_by_name(member_cluster_clients: List[MultiClusterClient]) -> Dict[str, int]:
    return {mcc.cluster_name: _pin(mcc) for mcc in member_cluster_clients[:2]}


def _source_seed_hosts(namespace: str) -> List[str]:
    return [
        f"{MDB_RESOURCE_NAME}-{SOURCE_CLUSTER_IDX}-{m}-svc.{namespace}.svc.cluster.local:27017"
        for m in range(SOURCE_MEMBERS)
    ]


def _cluster_entry(cluster_name: str, cluster_index: int | None = None) -> dict:
    """One spec.clusters[] entry; cluster_index=None leaves the pin unset."""
    entry: dict = {"name": cluster_name, "replicas": MONGOT_REPLICAS_PER_CLUSTER}
    if cluster_index is not None:
        entry["index"] = cluster_index
    return entry


def _full_clusters_spec(member_cluster_clients: List[MultiClusterClient], namespace: str) -> List[dict]:
    """spec.clusters[] in DELIBERATELY permuted array order: [cluster-2 pin 5, cluster-1 pin 2].
    Carries each cluster's managed-LB block (like conftest.build_per_cluster_search_crs): the operator
    reads LB mode off clusters[0], so an LB-less rewrite in the remove/re-add tests tears the Envoy down."""
    spec: List[dict] = []
    for mcc in reversed(member_cluster_clients[:2]):
        pin = _pin(mcc)
        spec.append(
            {
                "name": mcc.cluster_name,
                "index": pin,
                "replicas": MONGOT_REPLICAS_PER_CLUSTER,
                "loadBalancer": {
                    "managed": {
                        "replicas": ENVOY_LB_REPLICAS,
                        "externalHostname": search_resource_names.mc_proxy_svc_fqdn(MDBS_RESOURCE_NAME, namespace, pin),
                    },
                },
            }
        )
    return spec


def _entry_for_member(
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
    member_index: int,
) -> Tuple[MultiClusterClient, MongoDBSearch]:
    for mcc, mdbs in per_cluster_mdbs_search:
        if _idx(mcc) == member_index:
            return mcc, mdbs
    raise AssertionError(f"no per-cluster entry for harness member index {member_index}")


# ---------------------------------------------------------------------------
# Module helpers
# ---------------------------------------------------------------------------


def _assert_pin_resources_present_with_owner_labels(
    mcc: MultiClusterClient,
    namespace: str,
    mdbs_name: str,
    pin: int,
) -> None:
    """Positive anchor: every per-cluster Search resource exists under the PIN-derived
    name in this cluster AND carries the owner labels (search-name + search-namespace +
    cluster-name) a future label-based sweep needs to reap it.
    """
    expected_labels = {
        SEARCH_OWNER_NAME_LABEL: mdbs_name,
        SEARCH_OWNER_NAMESPACE_LABEL: namespace,
        SEARCH_CLUSTER_NAME_LABEL: mcc.cluster_name,
    }
    apps = mcc.apps_v1_api()
    sts_name = f"{mdbs_name}-search-{pin}"
    svc_name = f"{sts_name}-svc"
    proxy_svc_name = f"{sts_name}-proxy-svc"
    mongot_cm_name = f"{sts_name}-config"
    envoy_dep_name = search_resource_names.lb_deployment_name(mdbs_name, pin)
    envoy_cm_name = search_resource_names.lb_configmap_name(mdbs_name, pin)
    resources = {
        f"mongot STS {sts_name}": mcc.read_namespaced_stateful_set(sts_name, namespace),
        f"mongot Service {svc_name}": mcc.read_namespaced_service(svc_name, namespace),
        f"proxy Service {proxy_svc_name}": mcc.read_namespaced_service(proxy_svc_name, namespace),
        f"mongot ConfigMap {mongot_cm_name}": mcc.read_namespaced_config_map(mongot_cm_name, namespace),
        f"Envoy Deployment {envoy_dep_name}": apps.read_namespaced_deployment(envoy_dep_name, namespace),
        f"Envoy ConfigMap {envoy_cm_name}": mcc.read_namespaced_config_map(envoy_cm_name, namespace),
    }
    for what, obj in resources.items():
        labels = obj.metadata.labels or {}
        for key, want in expected_labels.items():
            assert labels.get(key) == want, (
                f"[{mcc.cluster_name}] {what}: label {key}={labels.get(key)!r}, want {want!r} — "
                f"owner labels are the only path back from a member resource to its CR"
            )
    logger.info(f"[{mcc.cluster_name}] all pin-{pin} resources present with owner labels {expected_labels}")


def _assert_indices_absent(mcc: MultiClusterClient, namespace: str, pin: int, other_pin: int) -> None:
    """Negative half of the pin-naming proof: no resources exist at the OTHER cluster's
    pin nor at either array position. Callers must anchor with the positive pin-named
    presence asserts first so an operator that created nothing cannot pass.
    """
    absent = sorted(({other_pin} | set(POSITIONAL_INDICES)) - {pin})
    for idx in absent:
        assert_foreign_resources_absent(mcc, MDBS_RESOURCE_NAME, idx, namespace)


def _wait_for_localize_skip_log(
    mcc: MultiClusterClient,
    namespace: str,
    search_name: str,
    timeout: int = 240,
) -> None:
    """Block until this cluster's simulated operator logs the LocalizeToCluster skip
    line for `search_name` — proof it OBSERVED a CR whose spec.clusters[] does not
    list it (vs. simply never reconciling).
    """
    core = mcc.core_v1_api()

    def check() -> tuple:
        pods = core.list_namespaced_pod(
            namespace, label_selector=f"app.kubernetes.io/name={SIMULATED_OPERATOR_NAME}"
        ).items
        if not pods:
            return False, f"no {SIMULATED_OPERATOR_NAME} pod found in {mcc.cluster_name}"
        for pod in pods:
            try:
                # istio-injected namespace: the pod has an istio-proxy sidecar, so the container must be named
                text = core.read_namespaced_pod_log(pod.metadata.name, namespace, container=SIMULATED_OPERATOR_NAME)
            except ApiException as exc:
                return False, f"cannot read {pod.metadata.name} logs: {exc.status}"
            for line in text.splitlines():
                if LOCALIZE_SKIP_LOG_SNIPPET in line and search_name in line:
                    return True, f"skip line in {pod.metadata.name}"
        return False, "localize-skip log line not found yet"

    run_periodically(
        check,
        timeout=timeout,
        sleep_time=5,
        msg=f"[{mcc.cluster_name}] operator localize-skip log for {search_name}",
    )


def _minimal_mc_search_cr(
    namespace: str,
    api_client: kubernetes.client.ApiClient,
    name: str,
    clusters_spec: List[dict],
) -> MongoDBSearch:
    """Schema-valid MC search CR (modulo the clusters[] under test) for the negative
    tests. Admission only runs the CRD's CEL rules + OpenAPI bounds (MongoDBSearch has no
    validating webhook), so the clusters[] rule under test is the only possible rejection;
    the load balancer / TLS are omitted — none of these CRs ever reconciles resources.
    """
    mdbs = MongoDBSearch.from_yaml(yaml_fixture("simulated-mc-search.yaml"), name=name, namespace=namespace)
    mdbs["spec"]["clusters"] = clusters_spec
    mdbs["spec"]["source"] = {
        "username": MONGOT_USER_NAME,
        "passwordSecretRef": {
            "name": f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password",
            "key": "password",
        },
        "external": {"hostAndPorts": _source_seed_hosts(namespace)},
    }
    mdbs.api = kubernetes.client.CustomObjectsApi(api_client)
    return mdbs


def _assert_admission_rejected(mdbs: MongoDBSearch, expected_substring: str) -> None:
    """Create must be rejected by the apiserver (CEL/schema) with `expected_substring`
    in the rejection message. A create that SUCCEEDS deletes the CR and fails the test.
    """
    try:
        mdbs.create()
    except ApiException as exc:
        body = str(exc.body)
        assert exc.status in (
            400,
            422,
        ), f"{mdbs.name}: expected an admission rejection (400/422), got status {exc.status}: {body}"
        assert (
            expected_substring in body
        ), f"{mdbs.name}: rejection message must contain {expected_substring!r}; got: {body}"
        logger.info(f"{mdbs.name}: rejected as expected with {expected_substring!r}")
        return
    mdbs.delete()
    raise AssertionError(f"{mdbs.name} was admitted but must be rejected with {expected_substring!r}")


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
    """Central MC operator scoped to MongoDB/User/MultiCluster (NOT mongodbsearch).

    All three members are passed: the pinned source on cluster-3 needs resource SAs there.
    """
    return install_central_mc_operator(
        namespace,
        central_cluster_name,
        multi_cluster_operator_installation_config,
        central_cluster_client,
        member_cluster_clients,
        member_cluster_names,
        watch_search=False,
    )


@fixture(scope="module")
def ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    """Issuer CA as both ConfigMap (ca-pem/mms-ca.crt — MongoDBMulti) and Secret (ca.crt —
    MongoDBSearch source); the Secret half is replicated to members in test_replicate_tls_to_members."""
    return create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)


@fixture(scope="module")
def mdb(
    namespace: str,
    member_cluster_names: List[str],
    central_cluster_client: kubernetes.client.ApiClient,
    ca_configmap: str,
) -> MongoDBMulti:
    """Single-member RS source with TLS+SCRAM, pinned to member cluster 3.

    The search setParameters quartet lets the source mongod serve $search; mongotHost
    is omitted here and flipped per-pin on the OM AC by the data-plane tests.
    """
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("simulated-mc-mongodb.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )
    resource["spec"]["clusterSpecList"] = cluster_spec_list(
        [member_cluster_names[SOURCE_MEMBER_INDEX]], [SOURCE_MEMBERS]
    )
    resource["spec"]["additionalMongodConfig"] = {"setParameter": dict(SEARCH_SET_PARAMETERS)}
    resource["spec"]["security"] = {
        "certsSecretPrefix": SOURCE_CERT_PREFIX,
        "tls": {"ca": ca_configmap},
        "authentication": {"enabled": True, "modes": ["SCRAM"]},
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


# admin_user / mdb_user / mongot_user fixtures come from the shared conftest and
# resolve this module's MDB_RESOURCE_NAME/MDBS_RESOURCE_NAME via request.module.


@fixture(scope="module")
def per_cluster_mdbs_search(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
    ca_configmap: str,
) -> List[Tuple[MultiClusterClient, MongoDBSearch]]:
    """Same CR applied to each search member's API, with spec.clusters[] in PERMUTED
    array order and non-positional pins ([cluster-2 pin 5, cluster-1 pin 2])."""
    search_clients = list(reversed(member_cluster_clients[:2]))
    return build_per_cluster_search_crs(
        namespace,
        search_clients,
        MDBS_RESOURCE_NAME,
        MONGOT_REPLICAS_PER_CLUSTER,
        lambda idx: search_resource_names.mc_proxy_svc_fqdn(MDBS_RESOURCE_NAME, namespace, idx),
        {
            "hostAndPorts": _source_seed_hosts(namespace),
            "tls": {"ca": {"name": ca_configmap}},
        },
        cluster_indices=_pins_by_name(member_cluster_clients),
    )


# ---------------------------------------------------------------------------
# Setup
# ---------------------------------------------------------------------------


@mark.e2e_search_simulated_mc_topology
def test_install_central_mc_operator(central_mc_operator: Operator):
    central_mc_operator.assert_is_running()


@mark.e2e_search_simulated_mc_topology
def test_install_source_tls_certificates(
    namespace: str,
    multi_cluster_issuer: str,
    mdb: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):
    """Explicit service_fqdns: the source-internal cluster index is 0 (single referenced
    cluster) while the hosting member's harness index is 2 — the helper's default
    SAN derivation would bake the harness index into the FQDNs."""
    service_fqdns = [f"{MDB_RESOURCE_NAME}-svc.{namespace}.svc.cluster.local"] + [
        host.rsplit(":", 1)[0] for host in _source_seed_hosts(namespace)
    ]
    create_multi_cluster_mongodb_tls_certs(
        multi_cluster_issuer,
        SOURCE_BUNDLE_SECRET,
        [member_cluster_clients[SOURCE_MEMBER_INDEX]],
        central_cluster_client,
        mdb,
        service_fqdns=service_fqdns,
    )


@mark.e2e_search_simulated_mc_topology
def test_mongodb_running(mdb: MongoDBMulti):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_search_simulated_mc_topology
def test_source_pinned_to_cluster_3(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    """Guard the facts every downstream seed host depends on: the source carries the
    source-internal index 0 (STS {mdb}-0) AND physically lives on member cluster 3."""
    source_mcc = member_cluster_clients[SOURCE_MEMBER_INDEX]
    assert _idx(source_mcc) == SOURCE_MEMBER_INDEX, (
        f"expected harness member index {SOURCE_MEMBER_INDEX} for the source cluster, "
        f"got {_idx(source_mcc)} ({source_mcc.cluster_name})"
    )
    sts_name = f"{MDB_RESOURCE_NAME}-{SOURCE_CLUSTER_IDX}"
    sts = source_mcc.read_namespaced_stateful_set(sts_name, namespace)
    assert (
        sts.status.ready_replicas or 0
    ) == SOURCE_MEMBERS, (
        f"[{source_mcc.cluster_name}] {sts_name} ready={sts.status.ready_replicas}, want {SOURCE_MEMBERS}"
    )
    logger.info(f"[{source_mcc.cluster_name}] source STS {sts_name} present and ready")


@mark.e2e_search_simulated_mc_topology
def test_create_users(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    admin_user: MongoDBUser,
    mdb_user: MongoDBUser,
    mongot_user: MongoDBUser,
):
    """The mongot password Secret is replicated to members with the other search
    Secrets in test_replicate_tls_to_members (before any search CR is applied)."""
    create_search_users(namespace, central_cluster_client, admin_user, mdb_user, mongot_user)


@mark.e2e_search_simulated_mc_topology
def test_install_simulated_operators_per_member(
    namespace: str,
    central_mc_operator: Operator,
    multi_cluster_operator_installation_config: dict,
    member_cluster_clients: List[MultiClusterClient],
):
    install_simulated_operators_per_member(
        namespace, multi_cluster_operator_installation_config, member_cluster_clients
    )


@mark.e2e_search_simulated_mc_topology
def test_deploy_lb_certificates(
    namespace: str,
    multi_cluster_issuer: str,
    member_cluster_clients: List[MultiClusterClient],
):
    """LB server cert SANs must cover the PIN-resolved proxy-svc FQDNs (…-2-…, …-5-…)."""
    lb_server_cert_name = search_resource_names.lb_server_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)
    lb_client_cert_name = search_resource_names.lb_client_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)

    server_domains = [
        f"{MDBS_RESOURCE_NAME}-search-{_pin(mcc)}-proxy-svc.{namespace}.svc.cluster.local"
        for mcc in member_cluster_clients[:2]
    ]

    create_tls_certs(
        issuer=multi_cluster_issuer,
        namespace=namespace,
        resource_name=search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, 0),
        replicas=ENVOY_LB_REPLICAS,
        service_name=server_domains[0].split(".")[0],
        additional_domains=server_domains,
        secret_name=lb_server_cert_name,
    )
    logger.info(f"LB server certificate created with SANs={server_domains}: {lb_server_cert_name}")

    create_tls_certs(
        issuer=multi_cluster_issuer,
        namespace=namespace,
        resource_name=f"{search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, 0)}-client",
        replicas=1,
        service_name=server_domains[0].split(".")[0],
        additional_domains=[f"*.{namespace}.svc.cluster.local"],
        secret_name=lb_client_cert_name,
    )
    logger.info(f"LB client certificate created: {lb_client_cert_name}")


@mark.e2e_search_simulated_mc_topology
def test_create_search_tls_certificate(
    namespace: str,
    multi_cluster_issuer: str,
    member_cluster_clients: List[MultiClusterClient],
):
    """mongot TLS cert SANs must carry the PIN-derived per-cluster service names."""
    secret_name = search_resource_names.mongot_tls_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)
    additional_domains: List[str] = []
    for mcc in member_cluster_clients[:2]:
        pin = _pin(mcc)
        additional_domains.append(f"{MDBS_RESOURCE_NAME}-search-{pin}-svc.{namespace}.svc.cluster.local")
        additional_domains.append(f"{MDBS_RESOURCE_NAME}-search-{pin}-proxy-svc.{namespace}.svc.cluster.local")

    create_tls_certs(
        issuer=multi_cluster_issuer,
        namespace=namespace,
        resource_name=search_resource_names.mongot_statefulset_name(MDBS_RESOURCE_NAME),
        secret_name=secret_name,
        additional_domains=additional_domains,
    )
    logger.info(f"mongot TLS certificate created with SANs={additional_domains}: {secret_name}")


@mark.e2e_search_simulated_mc_topology
def test_replicate_tls_to_members(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
):
    """Simulated-MC operators read only their own etcd — TLS material AND the mongot
    password Secret (spec.source.passwordSecretRef) must be present locally."""
    replicate_search_secrets_to_members(
        namespace=namespace,
        central_cluster_client=central_cluster_client,
        member_cluster_clients=member_cluster_clients[:2],
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        mdbs_tls_cert_prefix=MDBS_TLS_CERT_PREFIX,
        mongot_user_name=MONGOT_USER_NAME,
        ca_configmap_name=CA_CONFIGMAP_NAME,
    )


# ---------------------------------------------------------------------------
# (a) Permuted, non-positional pins
# ---------------------------------------------------------------------------


@mark.e2e_search_simulated_mc_topology
def test_search_running_per_cluster_with_permuted_pins(
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    apply_search_crs_and_assert_running(per_cluster_mdbs_search)


@mark.e2e_search_simulated_mc_topology
def test_resources_named_by_pin_not_position(
    namespace: str,
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    """Every per-cluster resource is named by the PIN (…-2-… in cluster-1, …-5-… in
    cluster-2), is ready, routes locally, and NOTHING exists at the other cluster's
    pin or at either array position (0/1)."""
    assert_per_cluster_count(per_cluster_mdbs_search)
    seed_hosts = _source_seed_hosts(namespace)
    pins = [_pin(mcc) for mcc, _ in per_cluster_mdbs_search]
    for mcc, mdbs in per_cluster_mdbs_search:
        pin = _pin(mcc)
        other_pin = next(p for p in pins if p != pin)
        sts_name = f"{MDBS_RESOURCE_NAME}-search-{pin}"
        sts = mcc.read_namespaced_stateful_set(sts_name, namespace)
        assert sts.spec.replicas == MONGOT_REPLICAS_PER_CLUSTER, (
            f"[{mcc.cluster_name}] {sts_name} expected {MONGOT_REPLICAS_PER_CLUSTER} replicas, "
            f"got {sts.spec.replicas}"
        )
        assert_workload_ready_in_cluster(
            mcc,
            namespace,
            {sts_name: MONGOT_REPLICAS_PER_CLUSTER},
            search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, pin),
        )
        _assert_pin_resources_present_with_owner_labels(mcc, namespace, MDBS_RESOURCE_NAME, pin)
        assert_mongot_sync_source_hosts(mcc, f"{MDBS_RESOURCE_NAME}-search-{pin}-config", namespace, seed_hosts)
        mdbs.assert_lb_status()
        _assert_indices_absent(mcc, namespace, pin, other_pin)
        logger.info(f"[{mcc.cluster_name}] resources named by pin {pin}; no positional/foreign-index leakage")

    # Envoy routes local-only at the PINNED indices. The helper resolves indices from
    # MultiClusterClient.cluster_index — the harness index (0/1) here — so pass
    # pin-indexed client wrappers: lookups, anchors and leak scan then use the pins.
    verify_per_cluster_envoy_sni(
        namespace=namespace,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        member_cluster_clients=[
            MultiClusterClient(mcc.api_client, mcc.cluster_name, _pin(mcc)) for mcc, _ in per_cluster_mdbs_search
        ],
        expected_upstreams_by_idx={
            _pin(mcc): [f"{MDBS_RESOURCE_NAME}-search-{_pin(mcc)}-svc.{namespace}.svc.cluster.local"]
            for mcc, _ in per_cluster_mdbs_search
        },
    )


@mark.e2e_search_simulated_mc_topology
def test_status_running_local_only(
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    assert_status_running_local_only(per_cluster_mdbs_search)


# ---------------------------------------------------------------------------
# (a2) Data plane: prove $search SERVES end-to-end through BOTH permuted pins.
#
# One source RS (cluster-3) feeds two independent mongots (pins 2/5 on clusters
# 1/2), each already syncing it. A single mongod has ONE mongotHost, so to
# exercise BOTH pins we FLIP the source's mongotHost between pin-2's and pin-5's
# Envoy proxy (the RS analogue of simulated_mc_sharded's flip): route at pin 2,
# verify APPLIED on disk, query; re-route at pin 5, verify, query. The $search
# index is created ONCE under the first flip and propagates to each mongot via its
# source sync, so the second flip only waits for that pin's mongot to report it
# READY — a propagation failure surfaces as a timeout, not an empty $search.
# ---------------------------------------------------------------------------

SEARCH_INDEX_READY_TIMEOUT = 300
SEARCH_QUERY_RETRY_TIMEOUT = 90


def _source_rs_search_tester(namespace: str, username: str, password: str) -> SearchTester:
    """SearchTester at the single-member source RS via ?replicaSet= so the driver
    discovers the primary for writes (restore + index) and reads.
    """
    seed = _source_seed_hosts(namespace)[0]
    conn_str = f"mongodb://{username}:{password}@{seed}/?replicaSet={MDB_RESOURCE_NAME}&authSource=admin"
    return SearchTester(conn_str, use_ssl=True, ca_path=get_issuer_ca_filepath())


def _pin_envoy_endpoint(namespace: str, pin: int) -> str:
    """A pin's Envoy proxy-svc FQDN:port — the mongotHost the source mongod dials."""
    return f"{MDBS_RESOURCE_NAME}-search-{pin}-proxy-svc.{namespace}.svc.cluster.local:{ENVOY_PROXY_PORT}"


def _flip_source_mongot_host(namespace: str, mdb: MongoDBMulti, pin: int) -> None:
    """Route the single source mongod at the pin's Envoy, then block until the agent
    applies it. Process name is {mdb}-{SOURCE_CLUSTER_IDX}-{member} (source-internal
    index 0); only those processes get the mongotHost.
    """
    endpoint = _pin_envoy_endpoint(namespace, pin)
    prefix = f"{MDB_RESOURCE_NAME}-{SOURCE_CLUSTER_IDX}-"

    def resolve_host(process_name: str) -> str | None:
        return endpoint if process_name.startswith(prefix) else None

    patch_mongot_host_via_ac(mdb, resolve_host)


def _assert_source_routed_to(namespace: str, source_mcc: MultiClusterClient, pin: int) -> None:
    """Verify the new mongotHost is APPLIED (don't patch-and-hope): each source mongod's
    on-disk automation-mongod.conf shows the pin's Envoy for mongotHost AND
    searchIndexManagementHostAndPort.
    """
    expected = _pin_envoy_endpoint(namespace, pin)

    def on_disk() -> tuple:
        all_ok = True
        msgs: List[str] = []
        for m in range(SOURCE_MEMBERS):
            pod_name = f"{MDB_RESOURCE_NAME}-{SOURCE_CLUSTER_IDX}-{m}"
            try:
                params = read_mongod_set_parameter(pod_name, namespace, source_mcc.api_client)
                got_host = params.get("mongotHost", "")
                got_mgmt = params.get("searchIndexManagementHostAndPort", "")
                if got_host != expected or got_mgmt != expected:
                    all_ok = False
                    msgs.append(
                        f"[{source_mcc.cluster_name}] {pod_name}: mongotHost={got_host!r} "
                        f"searchIndexMgmt={got_mgmt!r} expected={expected!r}"
                    )
                else:
                    msgs.append(f"[{source_mcc.cluster_name}] {pod_name}: mongotHost={expected} OK")
            except Exception as exc:
                all_ok = False
                msgs.append(f"[{source_mcc.cluster_name}] {pod_name}: error reading conf: {exc}")
        return all_ok, "\n".join(msgs)

    run_periodically(on_disk, timeout=300, sleep_time=10, msg=f"source mongotHost -> pin {pin}")
    logger.info(f"source routed to pin {pin}: {expected}")


def _route_and_query(
    namespace: str,
    mdb: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    pin: int,
    *,
    create_index: bool,
) -> None:
    """Flip the source at the pin's Envoy, verify APPLIED, then assert the deterministic
    $search row count via the source RS. create_index=True (first flip) creates the index
    (it needs searchIndexManagementHostAndPort, which only the flip sets); later flips
    reuse it (propagated via source sync) and only wait the pin's mongot READY.
    """
    source_mcc = member_cluster_clients[SOURCE_MEMBER_INDEX]
    _flip_source_mongot_host(namespace, mdb, pin)
    _assert_source_routed_to(namespace, source_mcc, pin)

    tester = _source_rs_search_tester(namespace, USER_NAME, USER_PASSWORD)
    movies = SampleMoviesSearchHelper(search_tester=tester)
    if create_index:
        movies.create_search_index()
    tester.wait_for_search_indexes_ready(movies.db_name, movies.col_name, timeout=SEARCH_INDEX_READY_TIMEOUT)
    # Deterministic compound query: the Hawaii/Alaska sample returns exactly 4 docs.
    movies.assert_search_query(retry_timeout=SEARCH_QUERY_RETRY_TIMEOUT)
    logger.info(f"pin {pin}: deterministic $search (==4) via source RS OK")


@mark.e2e_search_simulated_mc_topology
def test_restore_sample_database(namespace: str, tools_pod: ToolsPod):
    tester = _source_rs_search_tester(namespace, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
    tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )


@mark.e2e_search_simulated_mc_topology
def test_serve_search_through_pin_2(
    namespace: str,
    mdb: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    """Flip 1 of 2: route the source at pin 2's Envoy, create the index, prove $search serves."""
    _route_and_query(namespace, mdb, member_cluster_clients, PIN_BY_MEMBER_INDEX[0], create_index=True)


@mark.e2e_search_simulated_mc_topology
def test_serve_search_through_pin_5(
    namespace: str,
    mdb: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    """Flip 2 of 2: re-route at pin 5's Envoy, proving its mongot independently indexes +
    serves the shared source (index created once under pin 2)."""
    _route_and_query(namespace, mdb, member_cluster_clients, PIN_BY_MEMBER_INDEX[1], create_index=False)


# ---------------------------------------------------------------------------
# (b) Admission negatives + the operator-level single-entry-unpinned rejection
# ---------------------------------------------------------------------------


@mark.e2e_search_simulated_mc_topology
def test_admission_rejects_duplicate_pins(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    c1, c2 = (mcc.cluster_name for mcc in member_cluster_clients[:2])
    mdbs = _minimal_mc_search_cr(
        namespace,
        member_cluster_clients[0].api_client,
        f"{MDBS_RESOURCE_NAME}-neg",
        [_cluster_entry(c1, 3), _cluster_entry(c2, 3)],
    )
    _assert_admission_rejected(mdbs, "clusters[].index must be unique when set")


@mark.e2e_search_simulated_mc_topology
def test_admission_rejects_partial_pinning(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    c1, c2 = (mcc.cluster_name for mcc in member_cluster_clients[:2])
    mdbs = _minimal_mc_search_cr(
        namespace,
        member_cluster_clients[0].api_client,
        f"{MDBS_RESOURCE_NAME}-neg",
        [_cluster_entry(c1, 3), _cluster_entry(c2)],
    )
    _assert_admission_rejected(
        mdbs, "clusters[].index is required on every entry when more than one cluster is specified"
    )


@mark.e2e_search_simulated_mc_topology
def test_admission_rejects_out_of_range_pins(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    """index schema bounds: minimum 0, maximum 999. Both bounds are enforced
    server-side by the OpenAPI structural schema (the python client performs no
    client-side validation), so -1 is asserted rather than skipped."""
    c1, c2 = (mcc.cluster_name for mcc in member_cluster_clients[:2])

    over = _minimal_mc_search_cr(
        namespace,
        member_cluster_clients[0].api_client,
        f"{MDBS_RESOURCE_NAME}-neg",
        [_cluster_entry(c1, 1000), _cluster_entry(c2, 1)],
    )
    _assert_admission_rejected(over, "should be less than or equal to 999")

    under = _minimal_mc_search_cr(
        namespace,
        member_cluster_clients[0].api_client,
        f"{MDBS_RESOURCE_NAME}-neg",
        [_cluster_entry(c1, -1), _cluster_entry(c2, 1)],
    )
    _assert_admission_rejected(under, "should be greater than or equal to 0")


@mark.e2e_search_simulated_mc_topology
def test_admission_rejects_all_unpinned(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    """A multi-entry clusters[] with NO pins at all never reaches any operator: the
    requiredness CEL rule rejects it at admission."""
    c1, c2 = (mcc.cluster_name for mcc in member_cluster_clients[:2])
    mdbs = _minimal_mc_search_cr(
        namespace,
        member_cluster_clients[0].api_client,
        f"{MDBS_RESOURCE_NAME}-neg",
        [_cluster_entry(c1), _cluster_entry(c2)],
    )
    _assert_admission_rejected(
        mdbs, "clusters[].index is required on every entry when more than one cluster is specified"
    )


@mark.e2e_search_simulated_mc_topology
def test_single_entry_unpinned_rejected_by_simulated_operator(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    """An unpinned SINGLE-entry clusters[] passes admission (the requiredness CEL rule
    is vacuous at size<=1) and ValidateSpec (single-cluster), but simulated-MC mode
    requires the pin even on one entry: the LOCAL operator must surface
    ValidateSimulatedMCClusterIndices as phase Failed with its message."""
    local_mcc = member_cluster_clients[0]
    mdbs = _minimal_mc_search_cr(
        namespace,
        local_mcc.api_client,
        f"{MDBS_RESOURCE_NAME}-unpinned",
        [_cluster_entry(local_mcc.cluster_name)],
    )
    created = False
    try:
        mdbs.update()
        created = True
        mdbs.assert_reaches_phase(
            Phase.Failed,
            msg_regexp=r".*one operator per cluster requires index on every spec\.clusters\[\] entry \(missing on.*",
            timeout=300,
        )
        logger.info(f"{mdbs.name}: operator stamped Failed with the simulated-MC pin requirement")
    finally:
        if created:
            mdbs.delete()


# ---------------------------------------------------------------------------
# (c) Unlisted-operator no-op
# ---------------------------------------------------------------------------


@mark.e2e_search_simulated_mc_topology
def test_install_unlisted_simulated_operator(
    namespace: str,
    multi_cluster_operator_installation_config: dict,
    member_cluster_clients: List[MultiClusterClient],
):
    """Third simulated operator on the source cluster — its OPERATOR_CLUSTER_NAME is
    NOT in the search CR's spec.clusters[]."""
    operator = _install_simulated_operator(
        namespace,
        dict(multi_cluster_operator_installation_config),
        member_cluster_clients[SOURCE_MEMBER_INDEX],
    )
    operator.assert_is_running()


@mark.e2e_search_simulated_mc_topology
def test_unlisted_operator_is_noop(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    """Apply the SAME CR to the source cluster's etcd: its operator must observe it
    (localize-skip log), write NO status, create NO state ConfigMap, and materialise
    zero search resources — anchored by the listed clusters' slices being present."""
    assert_per_cluster_count(per_cluster_mdbs_search)
    source_mcc = member_cluster_clients[SOURCE_MEMBER_INDEX]

    # Positive co-anchors: the listed clusters DID materialise their pinned slices.
    for mcc, _mdbs in per_cluster_mdbs_search:
        _assert_pin_resources_present_with_owner_labels(mcc, namespace, MDBS_RESOURCE_NAME, _pin(mcc))

    src_mcc, src_mdbs = per_cluster_mdbs_search[0]
    src_mdbs.load()
    mdbs3 = MongoDBSearch.from_yaml(
        yaml_fixture("simulated-mc-search.yaml"), name=MDBS_RESOURCE_NAME, namespace=namespace
    )
    mdbs3["spec"] = copy.deepcopy(src_mdbs["spec"])
    mdbs3.api = kubernetes.client.CustomObjectsApi(source_mcc.api_client)
    try_load(mdbs3)
    mdbs3.update()
    logger.info(f"applied {MDBS_RESOURCE_NAME} (same spec as {src_mcc.cluster_name}) to {source_mcc.cluster_name}")

    _wait_for_localize_skip_log(source_mcc, namespace, MDBS_RESOURCE_NAME)

    mdbs3.load()
    phase = mdbs3.get_status_phase()
    assert phase is None, (
        f"[{source_mcc.cluster_name}] unlisted operator wrote status phase={phase} — "
        f"an operator whose cluster is not in spec.clusters[] must not touch the CR"
    )

    state_cm_name = search_resource_names.search_state_configmap_name(MDBS_RESOURCE_NAME)
    try:
        source_mcc.read_namespaced_config_map(state_cm_name, namespace)
        raise AssertionError(
            f"[{source_mcc.cluster_name}] state ConfigMap {state_cm_name} exists — the unlisted "
            f"operator must skip before initialising state"
        )
    except ApiException as exc:
        assert exc.status == 404, f"unexpected error reading {state_cm_name}: {exc.status}"

    # Zero search-owned resources: nothing at either pin nor at either array position.
    for idx in sorted({_pin(mcc) for mcc, _ in per_cluster_mdbs_search} | set(POSITIONAL_INDICES)):
        assert_foreign_resources_absent(source_mcc, MDBS_RESOURCE_NAME, idx, namespace)
    logger.info(f"[{source_mcc.cluster_name}] unlisted operator created nothing and wrote no status")


# ---------------------------------------------------------------------------
# (d) Remove / re-add lifecycle (current v0 contract: no cleanup on removal)
# ---------------------------------------------------------------------------


@mark.e2e_search_simulated_mc_topology
def test_remove_cluster_from_spec(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    """Remove cluster-1 (pin 2) from spec.clusters[] on every CR copy. v0 contract:
    cluster-2 keeps Running and serving; cluster-1's resources REMAIN in place with
    their owner labels (orphaned by design — a future sweep reaps by label)."""
    assert_per_cluster_count(per_cluster_mdbs_search)
    mcc_a, _mdbs_a = _entry_for_member(per_cluster_mdbs_search, 0)
    mcc_b, mdbs_b = _entry_for_member(per_cluster_mdbs_search, 1)
    pin_a, pin_b = _pin(mcc_a), _pin(mcc_b)

    removed_clusters = [
        c for c in _full_clusters_spec(member_cluster_clients, namespace) if c["name"] == mcc_b.cluster_name
    ]
    assert len(removed_clusters) == 1, f"expected exactly one remaining cluster entry, got {removed_clusters}"
    for _mcc, mdbs in per_cluster_mdbs_search:
        mdbs.load()
        mdbs["spec"]["clusters"] = removed_clusters
        mdbs.update()

    # Cluster-1's operator OBSERVED the removal (first localize-skip line in its log).
    _wait_for_localize_skip_log(mcc_a, namespace, MDBS_RESOURCE_NAME)

    # Cluster-2 keeps Running (generation-aware) and serving.
    mdbs_b.assert_reaches_phase(Phase.Running, timeout=600)
    assert_workload_ready_in_cluster(
        mcc_b,
        namespace,
        {f"{MDBS_RESOURCE_NAME}-search-{pin_b}": MONGOT_REPLICAS_PER_CLUSTER},
        search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, pin_b),
    )

    # Cluster-1's resources remain, still owner-labelled for a future label-based sweep.
    _assert_pin_resources_present_with_owner_labels(mcc_a, namespace, MDBS_RESOURCE_NAME, pin_a)
    # The skipped operator must not have fanned out the remaining cluster's slice either.
    _assert_indices_absent(mcc_a, namespace, pin_a, pin_b)

    logger.info(
        f"removal contract held: [{mcc_b.cluster_name}] serving at pin {pin_b}; "
        f"[{mcc_a.cluster_name}] pin-{pin_a} resources orphaned in place with owner labels"
    )


@mark.e2e_search_simulated_mc_topology
def test_readd_cluster_with_same_pin(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    """Re-add cluster-1 with the SAME pin: resources reconverge under the same names and
    Running returns on both clusters."""
    assert_per_cluster_count(per_cluster_mdbs_search)
    full_clusters = _full_clusters_spec(member_cluster_clients, namespace)
    for _mcc, mdbs in per_cluster_mdbs_search:
        mdbs.load()
        mdbs["spec"]["clusters"] = full_clusters
        mdbs.update()

    mcc_a, mdbs_a = _entry_for_member(per_cluster_mdbs_search, 0)
    _mcc_b, mdbs_b = _entry_for_member(per_cluster_mdbs_search, 1)
    mdbs_a.assert_reaches_phase(Phase.Running, timeout=900)
    mdbs_b.assert_reaches_phase(Phase.Running, timeout=600)

    pins = [_pin(mcc) for mcc, _ in per_cluster_mdbs_search]
    for mcc, _mdbs in per_cluster_mdbs_search:
        pin = _pin(mcc)
        assert_workload_ready_in_cluster(
            mcc,
            namespace,
            {f"{MDBS_RESOURCE_NAME}-search-{pin}": MONGOT_REPLICAS_PER_CLUSTER},
            search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, pin),
        )
        _assert_pin_resources_present_with_owner_labels(mcc, namespace, MDBS_RESOURCE_NAME, pin)
        other_pin = next(p for p in pins if p != pin)
        _assert_indices_absent(mcc, namespace, pin, other_pin)

    assert_status_running_local_only(per_cluster_mdbs_search)
    logger.info("re-add with the same pin reconverged under the original resource names")
