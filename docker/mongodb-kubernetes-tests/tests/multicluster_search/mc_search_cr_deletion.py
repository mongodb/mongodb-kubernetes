"""Centralized-MC CR-deletion e2e for MongoDBSearch (OnDelete path).

Owner references do not cross cluster boundaries, so Kubernetes GC alone cannot
clean up the per-cluster resources a centralized MongoDBSearch fans out to
member clusters; the operator's OnDelete sweep does it explicitly
(PVCs ride along via persistentVolumeClaimRetentionPolicy whenDeleted: Delete).
Presence is asserted per kind before the delete so the absence polling cannot
pass vacuously, and the same CR is re-created afterwards to prove the deletion
left no wreckage blocking reinstall.

Runs on the e2e_multi_cluster_2_clusters variant; the tools pod is pinned to
member cluster 0 for consistency with the 3-cluster add/remove test (where the
central cluster is outside the mesh).

The source is external, so the operator injects no search setParameters; the
test patches every source mongod's mongotHost via OM AC at cluster 0's
proxy-svc FQDN. That FQDN is stable across delete/re-create because per-cluster
indices come from spec.clusters[].index, so no re-patching is needed — and a
stale mongotHost never affects mongod health.
"""

from typing import List

import kubernetes
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
from tests.common.search import mc_search_helper, search_resource_names
from tests.common.search.mc_search_helper import (
    assert_source_mongot_host_observed,
    build_mongodb_user,
    create_mc_lb_certificates,
    create_mc_mongot_tls_cert,
    patch_source_mongot_host_via_om,
    replicate_search_secrets_to_members,
)
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.rs_search_helper import get_mc_rs_search_tester
from tests.common.search.search_deployment_helper import MCSearchDeploymentHelper
from tests.common.search.sharded_search_helper import create_issuer_ca, verify_text_search_query
from tests.multicluster.conftest import cluster_spec_list

logger = test_logger.get_test_logger(__name__)

MDB_RESOURCE_NAME = "mdb-mc-del"
MDBS_RESOURCE_NAME = "mdb-mc-del-search"

# The source MongoDBMulti RS spans both member clusters and stays fixed.
# Typed List[int | None] to match cluster_spec_list's signature.
SOURCE_MEMBERS_PER_CLUSTER: List[int | None] = [2, 2]

MONGOT_REPLICAS_PER_CLUSTER = 1
ENVOY_LB_REPLICAS = 2
ENVOY_PROXY_PORT = 27028

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
# Generous bound for the OnDelete cross-cluster sweep + STS PVC reaping.
DELETION_SWEEP_TIMEOUT = 300

MARKER = mark.e2e_mc_search_cr_deletion


def _build_search_resource(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    mdb: MongoDBMulti,
    ca_configmap: str,
) -> MongoDBSearch:
    """MongoDBSearch spec over the external MongoDBMulti source, spanning both clusters.

    A module function (not just a fixture body) so phase 5 can construct a FRESH
    unbound object after the CR has been deleted: try_load finds nothing, leaving
    the object unbound, so .update() creates it from scratch.
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
    clusters = []
    for mcc in member_cluster_clients:
        assert mcc.cluster_index is not None, f"cluster_index unset on {mcc.cluster_name!r}"
        clusters.append(
            {
                "name": mcc.cluster_name,
                "index": mcc.cluster_index,
                "replicas": MONGOT_REPLICAS_PER_CLUSTER,
                "loadBalancer": {
                    "managed": {
                        "externalHostname": search_resource_names.mc_proxy_svc_fqdn(
                            MDBS_RESOURCE_NAME, namespace, mcc.cluster_index
                        ),
                    },
                },
            }
        )
    resource["spec"]["clusters"] = clusters

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


# =============================================================================
# Fixtures
# =============================================================================


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
def stable_mongot_host(namespace: str) -> str:
    """Cluster 0's proxy-svc FQDN:port — the single mongotHost every source mongod uses.

    The proxy Service name is derived from the persisted cluster index, which is 0
    for the first cluster both before deletion and after re-create, so this target
    needs no re-patching across the CR delete/re-create cycle.
    """
    return f"{search_resource_names.mc_proxy_svc_fqdn(MDBS_RESOURCE_NAME, namespace, 0)}:{ENVOY_PROXY_PORT}"


@fixture(scope="module")
def tools_pod(namespace: str, member_cluster_clients: List[MultiClusterClient]) -> mongodb_tools_pod.ToolsPod:
    """mongorestore helper pod, pinned to member cluster 0.

    On e2e_multi_cluster_2_clusters the central cluster doubles as member 1, so the
    default client would also work — pinned anyway for consistency with the
    3-cluster add/remove test, where the central cluster is outside the mesh.
    """
    return mongodb_tools_pod.get_tools_pod(namespace, api_client=member_cluster_clients[0].api_client)


@fixture(scope="module")
def mdb(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
    ca_configmap: str,
) -> MongoDBMulti:
    """2-cluster MongoDBMulti RS source with TLS+SCRAM, fixed for the whole test.

    See q2_mc_rs_steady for why mongotHost is NOT set at spec level (the operator
    would re-apply it to every process every reconcile). The MongoDBSearch deletion
    under test must never touch this source.
    """
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("search-q2-mc-rs.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, SOURCE_MEMBERS_PER_CLUSTER)
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
    member_cluster_clients: List[MultiClusterClient],
    mdb: MongoDBMulti,
    ca_configmap: str,
) -> MongoDBSearch:
    return _build_search_resource(namespace, central_cluster_client, member_cluster_clients, mdb, ca_configmap)


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
# Phase 1: setup — source RS + MongoDBSearch to Running
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
    create_multi_cluster_mongodb_tls_certs(
        multi_cluster_issuer,
        SOURCE_BUNDLE_SECRET,
        member_cluster_clients,
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

    MCK does not replicate Secrets in production — that's the customer's job. The
    Secrets survive the CR deletion (they are customer-owned, not search-owned),
    which is also what lets phase 5 re-create without re-running this step.
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


@MARKER
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=900)


@MARKER
def test_patch_source_mongot_host(
    mdb: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    stable_mongot_host: str,
):
    """Point EVERY source mongod at cluster 0's proxy-svc FQDN via OM AC.

    For an external source the operator does not inject search setParameters, so
    the source mongods need mongotHost / searchIndexManagementHostAndPort set
    directly. A single stable target keeps the AC valid across the delete and
    re-create of the search CR (the per-index proxy Service name is identical
    before and after).
    """
    patch_source_mongot_host_via_om(mdb=mdb, mongot_host=stable_mongot_host)
    assert_source_mongot_host_observed(
        mdb=mdb, member_cluster_clients=member_cluster_clients, expected_mongot_host=stable_mongot_host
    )


# =============================================================================
# Phase 2: presence guard — the full per-cluster resource set exists
# =============================================================================


@MARKER
def test_full_resource_set_present_per_cluster(
    namespace: str,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """Every member cluster carries the complete, owner-labelled mongot + Envoy set
    AND its mongot data PVC(s). Phase 4 polls these exact kinds to absence — without
    this guard the deletion assertions could pass vacuously against a half-deployed
    search.
    """
    for mcc in member_cluster_clients:
        idx = helper.cluster_index(mcc.cluster_name)
        mc_search_helper.assert_cluster_search_resources_present(
            mcc=mcc, mdbs_resource_name=MDBS_RESOURCE_NAME, cluster_index=idx, namespace=namespace
        )

        pvcs = mc_search_helper.data_pvcs_for_cluster(mcc, MDBS_RESOURCE_NAME, idx, namespace)
        assert len(pvcs) == MONGOT_REPLICAS_PER_CLUSTER, (
            f"cluster {mcc.cluster_name} (idx={idx}): expected {MONGOT_REPLICAS_PER_CLUSTER} mongot data PVC(s), "
            f"found {[p.metadata.name for p in pvcs]}"
        )
        logger.info(f"cluster {mcc.cluster_name} (idx={idx}) data PVC(s): {[p.metadata.name for p in pvcs]}")


# =============================================================================
# Phase 3: light data-plane confidence
# =============================================================================


@MARKER
def test_create_search_index(mdb: MongoDBMulti, tools_pod):
    """Restore the sample dataset and create the $search index against the source RS."""
    tester = get_mc_rs_search_tester(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
    tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )

    user_tester = get_mc_rs_search_tester(mdb, USER_NAME, USER_PASSWORD)
    movies = SampleMoviesSearchHelper(search_tester=user_tester)
    movies.create_search_index()
    user_tester.wait_for_search_indexes_ready(movies.db_name, movies.col_name, timeout=SEARCH_INDEX_READY_TIMEOUT)


@MARKER
def test_search_serves(mdb: MongoDBMulti):
    """$search must return non-empty results — the deployment being deleted is real.
    Polled (generously) since mongot may briefly be in INITIAL_SYNC.
    """
    tester = get_mc_rs_search_tester(mdb, USER_NAME, USER_PASSWORD)
    verify_text_search_query(tester)


# =============================================================================
# Phase 4: delete the CR — everything search-owned must vanish everywhere
# =============================================================================


@MARKER
def test_delete_search_cr(mdbs: MongoDBSearch):
    mdbs.delete()

    def cr_gone():
        try:
            mdbs.load()
            return False, f"MongoDBSearch {mdbs.name} still present"
        except kubernetes.client.ApiException as e:
            if e.status == 404:
                return True, f"MongoDBSearch {mdbs.name} deleted"
            raise

    run_periodically(cr_gone, timeout=120, sleep_time=5, msg="MongoDBSearch CR deletion")


@MARKER
def test_all_member_clusters_fully_swept(
    namespace: str,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """ZERO search-owned objects remain in ANY member cluster: mongot StatefulSet,
    headless + proxy Services, mongot ConfigMap, data PVCs (STS retention policy),
    and the Envoy Deployment + ConfigMap. Each kind is polled independently so a
    partial sweep names the exact resource that leaked.
    """
    for mcc in member_cluster_clients:
        mc_search_helper.wait_for_cluster_search_resources_absent(
            mcc=mcc,
            mdbs_resource_name=MDBS_RESOURCE_NAME,
            cluster_index=helper.cluster_index(mcc.cluster_name),
            namespace=namespace,
            timeout=DELETION_SWEEP_TIMEOUT,
        )


@MARKER
def test_source_mongodb_untouched(
    mdb: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    """The search sweep must not touch the source MongoDBMulti: still Running, and
    its per-cluster StatefulSets present in both clusters at full member count.
    (The stale mongotHost in the AC is harmless for an external source — mongod
    health does not depend on the mongot target being resolvable.)
    """
    mdb.load()
    mdb.assert_reaches_phase(Phase.Running, timeout=120)

    statefulsets = mdb.read_statefulsets(member_cluster_clients)  # raises 404 if any STS was swept
    assert len(statefulsets) == len(member_cluster_clients)
    for i, (cluster_name, sts) in enumerate(statefulsets.items()):
        expected_members = SOURCE_MEMBERS_PER_CLUSTER[i]
        assert sts.spec.replicas == expected_members, (
            f"source STS {sts.metadata.name} in {cluster_name}: spec.replicas={sts.spec.replicas}, "
            f"want {expected_members}"
        )
        logger.info(
            f"source STS {sts.metadata.name} untouched in {cluster_name} "
            f"(replicas={sts.spec.replicas}, ready={sts.status.ready_replicas})"
        )


# =============================================================================
# Phase 5: re-create — deletion left nothing that blocks a fresh provision
# =============================================================================


@MARKER
def test_recreate_search_resource(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    mdb: MongoDBMulti,
    ca_configmap: str,
):
    """Re-create the same MongoDBSearch CR and drive it to Running. A fresh object
    is built (the module fixture is still bound to the deleted CR's stale state);
    try_load finds nothing post-delete, so .update() creates from scratch.
    """
    recreated = _build_search_resource(namespace, central_cluster_client, member_cluster_clients, mdb, ca_configmap)
    recreated.update()
    recreated.assert_reaches_phase(Phase.Running, timeout=900)


@MARKER
def test_recreated_resources_present_per_cluster(
    namespace: str,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """Every member cluster rematerializes its full mongot + Envoy resource set
    under the same per-cluster indices (indices come from spec.clusters[].index,
    so they are identical to the original layout across delete/re-create — keeping
    the source AC mongotHost target valid).
    """
    for mcc in member_cluster_clients:
        mc_search_helper.wait_for_cluster_search_resources_present(
            mcc=mcc,
            mdbs_resource_name=MDBS_RESOURCE_NAME,
            cluster_index=helper.cluster_index(mcc.cluster_name),
            namespace=namespace,
        )
