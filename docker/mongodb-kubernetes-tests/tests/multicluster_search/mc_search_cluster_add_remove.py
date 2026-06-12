"""Centralized-MC cluster add/remove e2e for MongoDBSearch (KUBE-114): add a
3rd search cluster, drop the middle one (full per-cluster teardown; the
persisted ClusterMapping keeps its index), then re-add it (fresh provision
under the ORIGINAL never-reused index, new PVC).

The external MongoDBMulti source is fixed throughout: ``spec.source.external``
is a flat seed list independent of ``spec.clusters[]``. Because the source is
external the operator injects no search setParameters; the test patches every
source mongod's mongotHost via OM AC at cluster 0's proxy-svc FQDN (cluster 0
is never dropped, so the target survives every membership change unpatched).

Requires 3 member clusters (e2e_multi_cluster_kind variant): the source RS
spans the first 2; the 3rd is the spare search cluster. The tools pod and the
mongotHost target are pinned to member clusters (central is outside the mesh).
"""

import json
from typing import Dict, List

import kubernetes
import pymongo.errors
from kubernetes.client import CoreV1Api
from kubetester import create_or_update_secret, try_load
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
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.multicluster.multicluster_utils import assert_deployment_ready_in_cluster
from tests.common.search import search_resource_names
from tests.common.search.mc_search_helper import (
    assert_cluster_envoy_sni,
    assert_search_owner_labels,
    assert_source_mongot_host_observed,
    build_mongodb_user,
    create_mc_lb_certificates,
    create_mc_mongot_tls_cert,
    patch_source_mongot_host_via_om,
    replicate_search_secrets_to_members,
)
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_deployment_helper import MCSearchDeploymentHelper
from tests.common.search.search_tester import SearchTester
from tests.common.search.sharded_search_helper import create_issuer_ca
from tests.conftest import get_issuer_ca_filepath
from tests.multicluster.conftest import cluster_spec_list

logger = test_logger.get_test_logger(__name__)

MDB_RESOURCE_NAME = "mdb-mc-add-rm"
MDBS_RESOURCE_NAME = "mdb-mc-add-rm-search"

# The source MongoDBMulti RS spans the first 2 member clusters and stays fixed.
# Typed List[int | None] to match cluster_spec_list's signature.
SOURCE_MEMBERS_PER_CLUSTER: List[int | None] = [2, 2]

MONGOT_REPLICAS_PER_CLUSTER = 1
ENVOY_LB_REPLICAS = 2
ENVOY_PROXY_PORT = 27028

# Owner-label helpers/constants are imported from mc_search_helper.

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"
USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"
MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"

MDBS_TLS_CERT_PREFIX = "certs"
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"
SOURCE_CERT_PREFIX = "clustercert"
SOURCE_BUNDLE_SECRET = f"{SOURCE_CERT_PREFIX}-{MDB_RESOURCE_NAME}-cert"

SEARCH_INDEX_READY_TIMEOUT = 300
SEARCH_QUERY_RETRY_TIMEOUT = 120

MARKER = mark.e2e_mc_search_cluster_add_remove


# =============================================================================
# Cluster-subset bookkeeping
# =============================================================================
#
# The test harness provisions 3 member clusters; `member_cluster_clients` lists
# all of them in persisted-index order. The MongoDBSearch starts with only the
# first 2 active in spec.clusters[], adds the 3rd, drops the middle, and re-adds
# it. We address clusters by their position in member_cluster_clients (which
# equals their persisted ClusterMapping index, since the harness sorts member
# clusters and the operator assigns indices in first-seen order).


def _clusters_spec(cluster_names: List[str]) -> List[Dict]:
    """spec.clusters[] payload: one mongot entry per named cluster."""
    return [{"clusterName": name, "replicas": MONGOT_REPLICAS_PER_CLUSTER} for name in cluster_names]


def _assert_search_owner_labels(obj_labels: Dict[str, str], cluster_name: str, where: str) -> None:
    """Bind MDBS_RESOURCE_NAME and delegate to the shared assertion."""
    assert_search_owner_labels(obj_labels, cluster_name, where, MDBS_RESOURCE_NAME)


def _mongot_config_name(cluster_index: int) -> str:
    return search_resource_names.mongot_configmap_name_for_cluster(MDBS_RESOURCE_NAME, cluster_index)


def _envoy_deployment_name(cluster_index: int) -> str:
    return search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, cluster_index)


def _envoy_configmap_name(cluster_index: int) -> str:
    return search_resource_names.lb_configmap_name(MDBS_RESOURCE_NAME, cluster_index)


def _data_pvc_prefix(cluster_index: int) -> str:
    """PVCs are "data-<mongot-sts>-<ordinal>"; the mongot data claim is "data"."""
    return f"data-{search_resource_names.mongot_statefulset_name_for_cluster(MDBS_RESOURCE_NAME, cluster_index)}-"


def _data_pvcs_for_cluster(mcc: MultiClusterClient, cluster_index: int, namespace: str) -> List:
    """All mongot data PVCs for a cluster index, sorted by name (one per ordinal)."""
    pvc_prefix = _data_pvc_prefix(cluster_index)
    pvcs = mcc.core_v1_api().list_namespaced_persistent_volume_claim(namespace)
    return sorted((p for p in pvcs.items if p.metadata.name.startswith(pvc_prefix)), key=lambda p: p.metadata.name)


# =============================================================================
# Existence / absence assertions for a cluster's full per-cluster resource set
# =============================================================================


def assert_cluster_search_resources_present(
    mcc: MultiClusterClient,
    cluster_index: int,
    namespace: str,
) -> None:
    """The named cluster carries a complete, owner-labelled mongot + Envoy set."""
    sts_name = search_resource_names.mongot_statefulset_name_for_cluster(MDBS_RESOURCE_NAME, cluster_index)
    svc_name = search_resource_names.mongot_service_name_for_cluster(MDBS_RESOURCE_NAME, cluster_index)
    proxy_svc_name = search_resource_names.mc_proxy_svc_name(MDBS_RESOURCE_NAME, cluster_index)
    cm_name = _mongot_config_name(cluster_index)
    envoy_deploy_name = _envoy_deployment_name(cluster_index)
    envoy_cm_name = _envoy_configmap_name(cluster_index)

    sts = mcc.read_namespaced_stateful_set(sts_name, namespace)
    headless = mcc.read_namespaced_service(svc_name, namespace)
    proxy = mcc.read_namespaced_service(proxy_svc_name, namespace)
    cm = mcc.read_namespaced_config_map(cm_name, namespace)
    _assert_search_owner_labels(sts.metadata.labels or {}, mcc.cluster_name, f"STS {sts_name}")
    _assert_search_owner_labels(headless.metadata.labels or {}, mcc.cluster_name, f"headless Service {svc_name}")
    _assert_search_owner_labels(proxy.metadata.labels or {}, mcc.cluster_name, f"proxy Service {proxy_svc_name}")
    _assert_search_owner_labels(cm.metadata.labels or {}, mcc.cluster_name, f"mongot CM {cm_name}")

    apps = mcc.apps_v1_api()
    assert_deployment_ready_in_cluster(apps, name=envoy_deploy_name, namespace=namespace)
    envoy_deploy = apps.read_namespaced_deployment(name=envoy_deploy_name, namespace=namespace)
    envoy_cm = mcc.read_namespaced_config_map(envoy_cm_name, namespace)
    _assert_search_owner_labels(
        envoy_deploy.metadata.labels or {}, mcc.cluster_name, f"Envoy Deployment {envoy_deploy_name}"
    )
    _assert_search_owner_labels(envoy_cm.metadata.labels or {}, mcc.cluster_name, f"Envoy CM {envoy_cm_name}")

    logger.info(
        f"cluster {mcc.cluster_name} (idx={cluster_index}) has full search resource set: "
        f"{sts_name}, {svc_name}, {proxy_svc_name}, {cm_name}, {envoy_deploy_name}, {envoy_cm_name}"
    )


def wait_for_cluster_search_resources_present(
    mcc: MultiClusterClient,
    cluster_index: int,
    namespace: str,
    timeout: int = 600,
) -> None:
    """Poll until the named cluster's mongot + Envoy resources have materialized."""

    def check():
        try:
            assert_cluster_search_resources_present(mcc, cluster_index, namespace)
            return True, f"cluster {mcc.cluster_name} (idx={cluster_index}) resources present"
        except (kubernetes.client.ApiException, AssertionError) as exc:
            return False, f"cluster {mcc.cluster_name} (idx={cluster_index}) not ready yet: {exc}"

    run_periodically(check, timeout=timeout, sleep_time=10, msg=f"cluster {mcc.cluster_name} search resources present")


def wait_for_cluster_search_resources_absent(
    mcc: MultiClusterClient,
    cluster_index: int,
    namespace: str,
    timeout: int = 600,
) -> None:
    """Poll until EVERY search-owned resource for the named cluster is gone.

    Covers mongot StatefulSet, headless + proxy Services, mongot ConfigMap, the
    backing PVCs (reaped by the StatefulSet's whenDeleted: Delete policy), and the
    Envoy Deployment + ConfigMap. Each kind is checked independently so a partial
    sweep surfaces the specific resource that leaked.
    """
    apps = mcc.apps_v1_api()
    core = mcc.core_v1_api()

    sts_name = search_resource_names.mongot_statefulset_name_for_cluster(MDBS_RESOURCE_NAME, cluster_index)
    svc_name = search_resource_names.mongot_service_name_for_cluster(MDBS_RESOURCE_NAME, cluster_index)
    proxy_svc_name = search_resource_names.mc_proxy_svc_name(MDBS_RESOURCE_NAME, cluster_index)
    cm_name = _mongot_config_name(cluster_index)
    envoy_deploy_name = _envoy_deployment_name(cluster_index)
    envoy_cm_name = _envoy_configmap_name(cluster_index)
    pvc_prefix = _data_pvc_prefix(cluster_index)

    def _read_gone(read_fn, name: str, kind: str):
        try:
            read_fn(name, namespace)
            return False, f"{kind} {name} still exists in {mcc.cluster_name}"
        except kubernetes.client.ApiException as e:
            if e.status == 404:
                return True, f"{kind} {name} deleted in {mcc.cluster_name}"
            raise

    def pvcs_gone():
        pvcs = core.list_namespaced_persistent_volume_claim(namespace)
        leftover = [p.metadata.name for p in pvcs.items if p.metadata.name.startswith(pvc_prefix)]
        if leftover:
            return False, f"PVC(s) for {mcc.cluster_name} still exist: {leftover}"
        return True, f"no PVCs with prefix {pvc_prefix!r} remain in {mcc.cluster_name}"

    checks = [
        (lambda: _read_gone(apps.read_namespaced_stateful_set, sts_name, "StatefulSet"), f"StatefulSet {sts_name}"),
        (
            lambda: _read_gone(core.read_namespaced_service, svc_name, "headless Service"),
            f"headless Service {svc_name}",
        ),
        (
            lambda: _read_gone(core.read_namespaced_service, proxy_svc_name, "proxy Service"),
            f"proxy Service {proxy_svc_name}",
        ),
        (lambda: _read_gone(core.read_namespaced_config_map, cm_name, "mongot ConfigMap"), f"mongot CM {cm_name}"),
        (
            lambda: _read_gone(apps.read_namespaced_deployment, envoy_deploy_name, "Envoy Deployment"),
            f"Envoy Deployment {envoy_deploy_name}",
        ),
        (
            lambda: _read_gone(core.read_namespaced_config_map, envoy_cm_name, "Envoy ConfigMap"),
            f"Envoy CM {envoy_cm_name}",
        ),
        (pvcs_gone, f"PVCs {pvc_prefix}*"),
    ]
    for check, label in checks:
        run_periodically(check, timeout=timeout, sleep_time=10, msg=f"{label} deletion in {mcc.cluster_name}")
        logger.info(f"{label} confirmed deleted in {mcc.cluster_name} (idx={cluster_index})")


def read_persisted_cluster_mapping(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> Dict[str, int]:
    """Return the operator's persisted ClusterMapping from the <name>-state ConfigMap."""
    state_cm_name = f"{MDBS_RESOURCE_NAME}-state"
    core = CoreV1Api(api_client=central_cluster_client)
    state_cm = core.read_namespaced_config_map(name=state_cm_name, namespace=namespace)
    raw = (state_cm.data or {}).get("state")
    assert raw, f"state ConfigMap {state_cm_name} missing 'state' key; got {list((state_cm.data or {}).keys())}"
    return json.loads(raw).get("clusterMapping") or {}


# =============================================================================
# Fixtures
# =============================================================================


@fixture(scope="module")
def all_cluster_names(member_cluster_clients: List[MultiClusterClient]) -> List[str]:
    return [mcc.cluster_name for mcc in member_cluster_clients]


@fixture(scope="module")
def source_cluster_names(all_cluster_names: List[str]) -> List[str]:
    """The first 2 clusters back the source MongoDBMulti RS."""
    return all_cluster_names[:2]


@fixture(scope="module")
def stable_mongot_host(namespace: str) -> str:
    """Cluster 0's proxy-svc FQDN:port — the single mongotHost every source mongod uses.

    Cluster 0 is never dropped across phases A-D, so this target stays valid through
    the add/drop/re-add of other clusters with no re-patching.
    """
    return f"{search_resource_names.mc_proxy_svc_fqdn(MDBS_RESOURCE_NAME, namespace, 0)}:{ENVOY_PROXY_PORT}"


@fixture(scope="module")
def tools_pod(namespace: str, member_cluster_clients: List[MultiClusterClient]) -> mongodb_tools_pod.ToolsPod:
    """mongorestore helper pod, pinned to member cluster 0.

    On e2e_multi_cluster_kind the central cluster is NOT a member and runs standalone
    istio in REGISTRY_ONLY mode, so a tools pod there can't resolve member service
    FQDNs. Member 0 is in the mesh and hosts the source RS, so the restore reaches it.
    """
    return mongodb_tools_pod.get_tools_pod(namespace, api_client=member_cluster_clients[0].api_client)


@fixture(scope="module")
def recorded_pvc() -> Dict[str, str]:
    """Carries the (name, uid) of cluster 1's mongot data PVC from Phase A to Phase D.

    Module-scoped so the value survives across test steps; lets Phase D assert the
    re-provisioned PVC has a DIFFERENT uid than the one reaped in Phase C.
    """
    return {}


@fixture(scope="module")
def helper(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
) -> MCSearchDeploymentHelper:
    return MCSearchDeploymentHelper(
        namespace=namespace,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        member_cluster_clients={mcc.cluster_name: mcc.core_v1_api() for mcc in member_cluster_clients},
    )


@fixture(scope="module")
def ca_configmap(
    issuer_ca_filepath: str,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> str:
    return create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME, api_client=central_cluster_client)


@fixture(scope="module")
def mdb(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    source_cluster_names: List[str],
    ca_configmap: str,
) -> MongoDBMulti:
    """2-cluster MongoDBMulti RS source with TLS+SCRAM, fixed for the whole test.

    See q2_mc_rs_steady for why mongotHost is NOT set at spec level (the operator
    would re-apply it to every process every reconcile). The search-cluster
    lifecycle under test never touches this source.
    """
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("search-q2-mc-rs.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )
    resource["spec"]["clusterSpecList"] = cluster_spec_list(source_cluster_names, SOURCE_MEMBERS_PER_CLUSTER)
    resource["spec"]["additionalMongodConfig"] = {
        "setParameter": {
            "skipAuthenticationToSearchIndexManagementServer": False,
            "skipAuthenticationToMongot": False,
            "searchTLSMode": "requireTLS",
            "useGrpcForSearch": True,
        },
    }
    resource["spec"]["security"] = {
        "certsSecretPrefix": SOURCE_CERT_PREFIX,
        "tls": {"ca": ca_configmap},
        "authentication": {"enabled": True, "modes": ["SCRAM"]},
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


@fixture(scope="module")
def mdbs(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    all_cluster_names: List[str],
    mdb: MongoDBMulti,
    ca_configmap: str,
) -> MongoDBSearch:
    """MongoDBSearch over the external MongoDBMulti source.

    Starts with the first 2 clusters active in spec.clusters[]; later phases
    mutate this list. The managed-LB externalHostname uses {clusterIndex} so all
    per-cluster names resolve to the matching proxy-svc FQDN.
    """
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-q2-mc-rs-search.yaml"),
        name=MDBS_RESOURCE_NAME,
        namespace=namespace,
    )

    seeds = [f"{svc}.{namespace}.svc.cluster.local:27017" for svc in mdb.service_names()]

    resource["spec"]["source"] = {
        "username": MONGOT_USER_NAME,
        "passwordSecretRef": {
            "name": f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password",
            "key": "password",
        },
        "external": {
            "hostAndPorts": seeds,
            "tls": {"ca": {"name": ca_configmap}},
        },
    }
    resource["spec"]["security"] = {
        "tls": {"certsSecretPrefix": MDBS_TLS_CERT_PREFIX},
    }
    resource["spec"]["loadBalancer"] = {
        "managed": {
            "externalHostname": (
                f"{MDBS_RESOURCE_NAME}-search-{{clusterIndex}}-proxy-svc.{namespace}.svc.cluster.local"
            ),
        },
    }
    # Start with the first 2 clusters active; phase B/C/D mutate this.
    resource["spec"]["clusters"] = _clusters_spec(all_cluster_names[:2])

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


@fixture(scope="module")
def admin_user(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBUser:
    return build_mongodb_user(
        yaml_filename="mongodbuser-mdb-admin.yaml",
        name=ADMIN_USER_NAME,
        username=ADMIN_USER_NAME,
        mdb_resource_name=MDB_RESOURCE_NAME,
        namespace=namespace,
        central_cluster_client=central_cluster_client,
    )


@fixture(scope="module")
def user(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBUser:
    return build_mongodb_user(
        yaml_filename="mongodbuser-mdb-user.yaml",
        name=USER_NAME,
        username=USER_NAME,
        mdb_resource_name=MDB_RESOURCE_NAME,
        namespace=namespace,
        central_cluster_client=central_cluster_client,
    )


@fixture(scope="module")
def mongot_user(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBUser:
    return build_mongodb_user(
        yaml_filename="mongodbuser-search-sync-source-user.yaml",
        name=f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}",
        username=MONGOT_USER_NAME,
        mdb_resource_name=MDB_RESOURCE_NAME,
        namespace=namespace,
        central_cluster_client=central_cluster_client,
    )


# =============================================================================
# Setup
# =============================================================================


@MARKER
def test_install_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@MARKER
def test_install_source_tls_certificates(
    multi_cluster_issuer: str,
    mdb: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):
    # Source-scoped: the MongoDBMulti spans only the first 2 clusters (search
    # spans all 3); the cert helper looks each client's cluster up in the
    # source's clusterSpecList and fails on the spare cluster.
    create_multi_cluster_mongodb_tls_certs(
        multi_cluster_issuer,
        SOURCE_BUNDLE_SECRET,
        member_cluster_clients[:2],
        central_cluster_client,
        mdb,
    )


@MARKER
def test_create_mdb_resource(mdb: MongoDBMulti):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=1500)


def _apply_user_password(
    user_resource: MongoDBUser,
    password: str,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> None:
    create_or_update_secret(
        namespace,
        name=user_resource["spec"]["passwordSecretKeyRef"]["name"],
        data={"password": password},
        api_client=central_cluster_client,
    )
    user_resource.update()


@MARKER
def test_create_user_credentials(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    admin_user: MongoDBUser,
    user: MongoDBUser,
    mongot_user: MongoDBUser,
):
    _apply_user_password(admin_user, ADMIN_USER_PASSWORD, namespace, central_cluster_client)
    admin_user.assert_reaches_phase(Phase.Updated, timeout=300)

    _apply_user_password(user, USER_PASSWORD, namespace, central_cluster_client)
    user.assert_reaches_phase(Phase.Updated, timeout=300)

    # mongot user needs searchCoordinator role from the MongoDBSearch CR; don't wait here.
    _apply_user_password(mongot_user, MONGOT_USER_PASSWORD, namespace, central_cluster_client)


@MARKER
def test_deploy_lb_certificates(
    namespace: str,
    multi_cluster_issuer: str,
    helper: MCSearchDeploymentHelper,
):
    """Managed-LB server + client certs covering EVERY cluster's proxy-svc FQDN.

    `helper` is built from all 3 member clusters, so the shared helper emits SANs
    for the spare cluster too — its cert must exist before it's added mid-test.
    """
    create_mc_lb_certificates(
        namespace=namespace,
        issuer=multi_cluster_issuer,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        tls_cert_prefix=MDBS_TLS_CERT_PREFIX,
        helper=helper,
        envoy_lb_replicas=ENVOY_LB_REPLICAS,
    )


@MARKER
def test_create_search_tls_certificate(
    namespace: str,
    multi_cluster_issuer: str,
    helper: MCSearchDeploymentHelper,
):
    """mongot TLS cert with SANs for every per-cluster mongot Service + proxy-svc
    FQDN across ALL 3 clusters, so the spare cluster's mongot can handshake on add.
    """
    create_mc_mongot_tls_cert(
        namespace=namespace,
        issuer=multi_cluster_issuer,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        tls_cert_prefix=MDBS_TLS_CERT_PREFIX,
        helper=helper,
    )


@MARKER
def test_replicate_secrets_to_members(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
):
    """Replicate TLS Secrets, mongot password, and CA to every member cluster.

    MCK does not replicate Secrets in production — that's the customer's job.
    Replicating to ALL 3 clusters lets the spare cluster's mongot start on add.
    """
    replicate_search_secrets_to_members(
        namespace=namespace,
        central_cluster_client=central_cluster_client,
        member_cluster_clients=member_cluster_clients,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        mdbs_tls_cert_prefix=MDBS_TLS_CERT_PREFIX,
        mongot_user_name=MONGOT_USER_NAME,
        ca_configmap_name=CA_CONFIGMAP_NAME,
    )


# =============================================================================
# Phase A: 2 search clusters (indices 0, 1)
# =============================================================================


@MARKER
def test_phaseA_create_search_resource(mdbs: MongoDBSearch):
    """MongoDBSearch with the first 2 clusters reaches Running."""
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=900)


@MARKER
def test_phaseA_verify_initial_two_clusters(
    namespace: str,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """Indices 0 and 1 carry the full mongot + Envoy resource set; index 2 has nothing yet."""
    for mcc in member_cluster_clients[:2]:
        assert_cluster_search_resources_present(mcc, helper.cluster_index(mcc.cluster_name), namespace)

    spare = member_cluster_clients[2]
    wait_for_cluster_search_resources_absent(spare, helper.cluster_index(spare.cluster_name), namespace, timeout=60)


@MARKER
def test_phaseA_record_cluster1_pvc(
    namespace: str,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
    recorded_pvc: Dict[str, str],
):
    """Record cluster 1's mongot data PVC (name, uid) so Phase D can prove the
    re-provisioned PVC is brand-new (different uid), not the original adopted back.
    """
    cluster1 = member_cluster_clients[1]
    idx = helper.cluster_index(cluster1.cluster_name)
    pvcs = _data_pvcs_for_cluster(cluster1, idx, namespace)
    assert pvcs, f"no mongot data PVC found for cluster {cluster1.cluster_name} (idx={idx}) to record"
    recorded_pvc["name"] = pvcs[0].metadata.name
    recorded_pvc["uid"] = pvcs[0].metadata.uid
    logger.info(f"recorded cluster-1 PVC {recorded_pvc['name']} uid={recorded_pvc['uid']}")


@MARKER
def test_phaseA_patch_source_mongot_host(
    mdb: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    stable_mongot_host: str,
):
    """Point EVERY source mongod at cluster 0's proxy-svc FQDN via OM AC.

    For an external source the operator does not inject search setParameters, so the
    source mongods need mongotHost / searchIndexManagementHostAndPort set directly. We
    use a SINGLE stable target (cluster 0, never dropped) rather than per-cluster
    locality: source mongods live in clusters 0 and 1, and Phase C drops search cluster
    1 — a per-cluster mapping would leave cluster-1's mongod pointing at a deleted proxy
    Service. The stable target survives add/drop/re-add with no re-patching.
    """
    patch_source_mongot_host_via_om(mdb=mdb, mongot_host=stable_mongot_host)
    assert_source_mongot_host_observed(
        mdb=mdb, member_cluster_clients=member_cluster_clients[:2], expected_mongot_host=stable_mongot_host
    )


@MARKER
def test_phaseA_create_search_index(mdb: MongoDBMulti, tools_pod):
    """Restore the sample dataset and create the $search index against the source RS."""
    tester = _search_tester(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
    tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )

    user_tester = _search_tester(mdb, USER_NAME, USER_PASSWORD)
    movies = SampleMoviesSearchHelper(search_tester=user_tester)
    movies.create_search_index()
    user_tester.wait_for_search_indexes_ready(movies.db_name, movies.col_name, timeout=SEARCH_INDEX_READY_TIMEOUT)


@MARKER
def test_phaseA_search_serves(mdb: MongoDBMulti):
    _assert_search_serves(mdb)


# =============================================================================
# Phase B: ADD a 3rd cluster (index 2)
# =============================================================================


@MARKER
def test_phaseB_add_third_cluster(
    mdbs: MongoDBSearch,
    all_cluster_names: List[str],
):
    """Append the 3rd cluster to spec.clusters[] and reconcile to Running."""
    mdbs.load()
    mdbs["spec"]["clusters"] = _clusters_spec(all_cluster_names[:3])
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=900)


@MARKER
def test_phaseB_third_cluster_materialized(
    namespace: str,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """The added cluster gets its full resource set under the next free index (2),
    and the pre-existing clusters (0, 1) are untouched.
    """
    added = member_cluster_clients[2]
    wait_for_cluster_search_resources_present(added, helper.cluster_index(added.cluster_name), namespace)

    for mcc in member_cluster_clients[:2]:
        assert_cluster_search_resources_present(mcc, helper.cluster_index(mcc.cluster_name), namespace)


@MARKER
def test_phaseB_search_still_serves(mdb: MongoDBMulti):
    _assert_search_serves(mdb)


# =============================================================================
# Phase C: DROP the middle cluster (index 1)
# =============================================================================


@MARKER
def test_phaseC_drop_middle_cluster(
    mdbs: MongoDBSearch,
    all_cluster_names: List[str],
):
    """Remove the middle cluster (index 1) from spec.clusters[], leaving 0 and 2."""
    remaining = [all_cluster_names[0], all_cluster_names[2]]
    mdbs.load()
    mdbs["spec"]["clusters"] = _clusters_spec(remaining)
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=900)


@MARKER
def test_phaseC_dropped_cluster_fully_swept(
    namespace: str,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """Every search-owned resource for the dropped cluster (index 1) is gone:
    mongot kinds, proxy Svc, PVCs, Envoy Deployment + ConfigMap. The surviving
    clusters (0, 2) keep their full resource sets.
    """
    dropped = member_cluster_clients[1]
    wait_for_cluster_search_resources_absent(dropped, helper.cluster_index(dropped.cluster_name), namespace)

    for mcc in (member_cluster_clients[0], member_cluster_clients[2]):
        assert_cluster_search_resources_present(mcc, helper.cluster_index(mcc.cluster_name), namespace)


@MARKER
def test_phaseC_surviving_envoy_sni_resolved(
    namespace: str,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """Each surviving cluster's Envoy lds.json SNI resolves to its OWN proxy-svc FQDN
    with no ``{clusterIndex}`` placeholder residue.

    After a non-last cluster is dropped, the managed-LB externalHostname template must
    be substituted with each cluster's PERSISTED index, not its spec position — a
    position-based resolution would point cluster 2 (persisted idx 2) at idx-1's name.
    """
    for mcc in (member_cluster_clients[0], member_cluster_clients[2]):
        assert_cluster_envoy_sni(
            mcc=mcc,
            mdbs_resource_name=MDBS_RESOURCE_NAME,
            cluster_index=helper.cluster_index(mcc.cluster_name),
            namespace=namespace,
        )


@MARKER
def test_phaseC_mapping_retains_dropped_index(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """The persisted ClusterMapping still records the dropped cluster's index —
    indices are monotonic and never reused (api/.../cluster_index.go).
    """
    dropped = member_cluster_clients[1]
    expected_idx = helper.cluster_index(dropped.cluster_name)
    mapping = read_persisted_cluster_mapping(namespace, central_cluster_client)
    assert mapping.get(dropped.cluster_name) == expected_idx, (
        f"clusterMapping[{dropped.cluster_name!r}]={mapping.get(dropped.cluster_name)!r}, "
        f"want retained index {expected_idx}; full mapping={mapping}"
    )
    logger.info(f"persisted mapping retains dropped cluster's index: {mapping}")


@MARKER
def test_phaseC_search_still_serves(mdb: MongoDBMulti):
    _assert_search_serves(mdb)


# =============================================================================
# Phase D: RE-ADD the dropped cluster (original index 1, new PVC)
# =============================================================================


@MARKER
def test_phaseD_readd_dropped_cluster(
    mdbs: MongoDBSearch,
    all_cluster_names: List[str],
):
    """Re-add the previously dropped cluster to spec.clusters[]."""
    mdbs.load()
    mdbs["spec"]["clusters"] = _clusters_spec(all_cluster_names[:3])
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=900)


@MARKER
def test_phaseD_readd_uses_original_index_and_fresh_pvc(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
    recorded_pvc: Dict[str, str],
):
    """The re-added cluster provisions under its ORIGINAL index with a brand-new PVC.

    Index reuse is proven against the persisted mapping. PVC freshness is proven by
    comparing the data PVC's uid against the one recorded in Phase A: the Phase-C sweep
    reaped the original, so a present PVC with a DIFFERENT uid was created by the
    re-provision, not the original adopted back. (A same-name PVC can still be a
    different object; the uid is the unambiguous identity check.)
    """
    readded = member_cluster_clients[1]
    original_idx = helper.cluster_index(readded.cluster_name)

    wait_for_cluster_search_resources_present(readded, original_idx, namespace)

    mapping = read_persisted_cluster_mapping(namespace, central_cluster_client)
    assert mapping.get(readded.cluster_name) == original_idx, (
        f"re-added cluster {readded.cluster_name!r} got index {mapping.get(readded.cluster_name)!r}, "
        f"want ORIGINAL {original_idx}; full mapping={mapping}"
    )

    pvcs = _data_pvcs_for_cluster(readded, original_idx, namespace)
    assert pvcs, f"expected a freshly-provisioned PVC for cluster {readded.cluster_name} after re-add; found none"
    new_uids = {p.metadata.uid for p in pvcs}
    assert recorded_pvc.get("uid"), "Phase A did not record the original cluster-1 PVC uid"
    assert recorded_pvc["uid"] not in new_uids, (
        f"re-added cluster {readded.cluster_name} reused the ORIGINAL PVC uid {recorded_pvc['uid']!r} "
        f"(should be reaped + re-provisioned); current PVC uids={new_uids}"
    )
    logger.info(
        f"re-added cluster {readded.cluster_name} reused original index {original_idx} "
        f"with fresh PVC(s) {[p.metadata.name for p in pvcs]} uids={new_uids} "
        f"(original reaped uid={recorded_pvc['uid']})"
    )


@MARKER
def test_phaseD_all_envoy_sni_resolved(
    namespace: str,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """After re-add, every cluster's Envoy lds.json SNI resolves to its own per-index
    proxy-svc FQDN with no placeholder residue — including the re-added cluster at its
    original index.
    """
    for mcc in member_cluster_clients:
        assert_cluster_envoy_sni(
            mcc=mcc,
            mdbs_resource_name=MDBS_RESOURCE_NAME,
            cluster_index=helper.cluster_index(mcc.cluster_name),
            namespace=namespace,
        )


@MARKER
def test_phaseD_search_serves_all_clusters(mdb: MongoDBMulti):
    _assert_search_serves(mdb)


# =============================================================================
# Data-plane helpers
# =============================================================================


def _search_tester(mdb: MongoDBMulti, username: str, password: str) -> SearchTester:
    """SearchTester pointed at the source MongoDBMulti RS via mesh DNS."""
    seed_host = f"{mdb.name}-0-0-svc.{mdb.namespace}.svc.cluster.local:27017"
    conn_str = f"mongodb://{username}:{password}@{seed_host}/?replicaSet={mdb.name}&authSource=admin"
    return SearchTester(conn_str, use_ssl=True, ca_path=get_issuer_ca_filepath())


def _assert_search_serves(mdb: MongoDBMulti) -> None:
    """$search must return non-empty results. Wrapped in run_periodically since
    mongot may briefly be in INITIAL_SYNC after a topology change.
    """
    tester = _search_tester(mdb, USER_NAME, USER_PASSWORD)
    movies = SampleMoviesSearchHelper(search_tester=tester)

    def execute_search():
        try:
            results = movies.text_search_movies("Star Wars")
            count = len(results)
            if count > 0:
                return True, f"$search returned {count} results"
            return False, "$search returned no results"
        except pymongo.errors.PyMongoError as exc:
            return False, f"$search error: {exc}"

    run_periodically(execute_search, timeout=SEARCH_QUERY_RETRY_TIMEOUT, sleep_time=5, msg="$search query to succeed")
