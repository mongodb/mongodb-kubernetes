"""E2E: Verify pendingMongotGroups status and fallback routing in envoy config."""
from __future__ import annotations

import json

from kubetester.kubetester import KubernetesTester, run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from pytest import mark
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.bootstrap_test_mixins import (
    InstallOperatorTests,
    MongoDBDeploymentConfig,
    MongoDBShardedDeploymentTests,
    SearchDeploymentConfig,
    SearchShardedDeploymentTests,
)
from tests.conftest import get_namespace

logger = test_logger.get_test_logger(__name__)

MARKER = mark.e2e_search_pending_mongot_groups
NAMESPACE = get_namespace()
MDB_RESOURCE_NAME = "mdb-sh-pending"
MDBS_RESOURCE_NAME = MDB_RESOURCE_NAME
INITIAL_SHARD_COUNT = 2
SCALED_UP_SHARD_COUNT = 3
MDB = MongoDBDeploymentConfig(mdb_resource_name=MDB_RESOURCE_NAME, shard_count=INITIAL_SHARD_COUNT)
SEARCH = SearchDeploymentConfig()


def _load_mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch(name=MDBS_RESOURCE_NAME, namespace=namespace)
    resource.load()
    return resource


def _get_pending_mongot_groups(namespace: str) -> list:
    mdbs = _load_mdbs(namespace)
    try:
        return mdbs["status"]["pendingMongotGroups"]
    except (KeyError, TypeError):
        return []


def _get_envoy_configmap_data(namespace: str) -> dict:
    cm_name = search_resource_names.lb_configmap_name(MDBS_RESOURCE_NAME)
    return KubernetesTester.read_configmap(namespace, cm_name)


def _get_lds_config(namespace: str) -> dict:
    data = _get_envoy_configmap_data(namespace)
    return json.loads(data["lds.json"])


def _get_cds_config(namespace: str) -> dict:
    data = _get_envoy_configmap_data(namespace)
    return json.loads(data["cds.json"])


def _find_filter_chain_for_shard(lds_config: dict, shard_name: str) -> dict | None:
    """Find the filter chain matching a shard by looking at cluster name in route."""
    shard_safe = shard_name.replace("-", "_")
    for resource in lds_config.get("resources", []):
        for fc in resource.get("filter_chains", []):
            for f in fc.get("filters", []):
                tc = f.get("typed_config", {})
                rc = tc.get("route_config", {})
                for vh in rc.get("virtual_hosts", []):
                    for route in vh.get("routes", []):
                        cluster = route.get("route", {}).get("cluster", "")
                        if shard_safe in cluster or cluster == "mongot_cluster_level_cluster":
                            # Check SNI match to identify the shard
                            fcm = fc.get("filter_chain_match", {})
                            server_names = fcm.get("server_names", [])
                            if any(shard_name in sn for sn in server_names):
                                return fc
                            # Non-TLS: single filter chain
                            if not server_names and shard_safe in cluster:
                                return fc
    return None


def _get_route_cluster_and_headers(fc: dict) -> tuple:
    """Extract (cluster_name, headers_to_add) from a filter chain."""
    for f in fc.get("filters", []):
        tc = f.get("typed_config", {})
        rc = tc.get("route_config", {})
        for vh in rc.get("virtual_hosts", []):
            for route in vh.get("routes", []):
                cluster = route.get("route", {}).get("cluster", "")
                headers = route.get("request_headers_to_add", [])
                return cluster, headers
    return "", []


def _assert_normal_routing(lds_config: dict, shard_name: str):
    """Assert shard has normal per-shard routing (no fallback)."""
    shard_safe = shard_name.replace("-", "_")
    expected_cluster = f"mongot_{shard_safe}_cluster"
    fc = _find_filter_chain_for_shard(lds_config, shard_name)
    assert fc is not None, f"No filter chain found for shard {shard_name}"
    cluster, headers = _get_route_cluster_and_headers(fc)
    assert cluster == expected_cluster, (
        f"Shard {shard_name}: expected cluster {expected_cluster}, got {cluster}"
    )
    assert not headers, f"Shard {shard_name}: unexpected headers {headers}"


def _assert_fallback_routing(lds_config: dict, shard_name: str):
    """Assert shard has fallback routing to cluster-level cluster."""
    fc = _find_filter_chain_for_shard(lds_config, shard_name)
    assert fc is not None, f"No filter chain found for shard {shard_name}"
    cluster, headers = _get_route_cluster_and_headers(fc)
    assert cluster == "mongot_cluster_level_cluster", (
        f"Shard {shard_name}: expected mongot_cluster_level_cluster, got {cluster}"
    )
    header_keys = [h.get("header", {}).get("key", "") for h in headers]
    assert "search-envoy-metadata-bin" in header_keys, (
        f"Shard {shard_name}: missing search-envoy-metadata-bin header"
    )


# ===========================================================================
# Phase 1: Bootstrap (operator + sharded MongoDB + MongoDBSearch)
# ===========================================================================


class TestInstallOperator(InstallOperatorTests):
    pass


class TestMongoDBShardedDeployment(MongoDBShardedDeploymentTests):
    namespace = NAMESPACE
    mdb_config = MDB
    search_config = SEARCH


class TestSearchShardedDeployment(SearchShardedDeploymentTests):
    namespace = NAMESPACE
    mdb_config = MDB
    search_config = SEARCH


# ===========================================================================
# Phase 2: After initial deploy reaches Running, pendingMongotGroups is empty
# ===========================================================================


@MARKER
def test_initial_pending_groups_empty(namespace: str):
    """After CR reaches Running, all mongot groups are ready so pending list is empty."""
    pending = _get_pending_mongot_groups(namespace)
    assert pending == [] or pending is None, (
        f"Expected empty pendingMongotGroups after Running, got {pending}"
    )
    logger.info("pendingMongotGroups is empty after initial deploy (all mongots ready)")


@MARKER
def test_initial_envoy_config_normal_routing(namespace: str):
    """All initial shards should have normal per-shard routing (no fallback)."""
    lds_config = _get_lds_config(namespace)
    for i in range(INITIAL_SHARD_COUNT):
        shard_name = f"{MDB_RESOURCE_NAME}-{i}"
        _assert_normal_routing(lds_config, shard_name)
        logger.info(f"Shard {shard_name}: normal routing confirmed")


# ===========================================================================
# Phase 3: Scale up — new shard should be pending with fallback routing
# ===========================================================================


@MARKER
def test_scale_up_create_shard_certs(namespace: str):
    """Create TLS certificates for the new shard before scaling."""
    from kubetester.certs import ISSUER_CA_NAME, create_mongodb_tls_certs, create_tls_certs
    from tests.common.search.search_deployment_helper import ensure_search_issuer
    from tests.common.search.sharded_search_helper import create_lb_certificates

    shard_idx = SCALED_UP_SHARD_COUNT - 1
    shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"

    # MongoDB shard TLS cert
    secret_name = f"mdb-sh-{MDB_RESOURCE_NAME}-{shard_idx}-cert"
    create_mongodb_tls_certs(
        issuer=ISSUER_CA_NAME,
        namespace=namespace,
        resource_name=f"{MDB_RESOURCE_NAME}-{shard_idx}",
        bundle_secret_name=secret_name,
        replicas=MDB.mongods_per_shard,
        service_name=f"{MDB_RESOURCE_NAME}-sh",
    )

    # Search TLS cert for new shard
    issuer = ensure_search_issuer(namespace)
    search_cert_name = search_resource_names.shard_tls_cert_name(
        MDBS_RESOURCE_NAME, shard_name, SEARCH.tls_cert_prefix
    )
    additional_domains = [
        f"{search_resource_names.shard_service_name(MDBS_RESOURCE_NAME, shard_name)}.{namespace}.svc.cluster.local",
        f"{search_resource_names.shard_proxy_service_name(MDBS_RESOURCE_NAME, shard_name)}.{namespace}.svc.cluster.local",
    ]
    create_tls_certs(
        issuer=issuer,
        namespace=namespace,
        resource_name=search_resource_names.shard_statefulset_name(MDBS_RESOURCE_NAME, shard_name),
        secret_name=search_cert_name,
        additional_domains=additional_domains,
    )

    # Recreate LB certs with SANs for all shards
    create_lb_certificates(
        namespace, issuer, SCALED_UP_SHARD_COUNT,
        MDB_RESOURCE_NAME, MDBS_RESOURCE_NAME, SEARCH.tls_cert_prefix,
    )
    logger.info(f"Certificates created for new shard {shard_name}")


@MARKER
def test_scale_up_update_shard_count(namespace: str):
    """Scale MongoDB from 2 to 3 shards."""
    mdb = MongoDB(name=MDB_RESOURCE_NAME, namespace=namespace)
    mdb.load()
    mdb["spec"]["shardCount"] = SCALED_UP_SHARD_COUNT
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)
    logger.info(f"MongoDB scaled up to {SCALED_UP_SHARD_COUNT} shards")


@MARKER
def test_scale_up_update_search_source(namespace: str):
    """Add the new shard to MongoDBSearch source spec so it creates the mongot group."""
    mdbs = _load_mdbs(namespace)
    shard_idx = SCALED_UP_SHARD_COUNT - 1
    new_shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
    new_shard_entry = {
        "shardName": new_shard_name,
        "hosts": [
            f"{new_shard_name}-{m}.{MDB_RESOURCE_NAME}-sh.{namespace}.svc.cluster.local:27017"
            for m in range(MDB.mongods_per_shard)
        ],
    }
    mdbs["spec"]["source"]["external"]["shardedCluster"]["shards"].append(new_shard_entry)
    mdbs.update()
    logger.info(f"Added shard {new_shard_name} to MongoDBSearch source spec")


@MARKER
def test_scale_up_new_shard_in_pending(namespace: str):
    """Immediately after scale-up, the new shard should be in pendingMongotGroups.

    We poll because the search controller may take a moment to reconcile.
    """
    new_shard = f"{MDB_RESOURCE_NAME}-{SCALED_UP_SHARD_COUNT - 1}"

    def check():
        pending = _get_pending_mongot_groups(namespace)
        if pending and new_shard in pending:
            return True, f"New shard {new_shard} found in pendingMongotGroups: {pending}"
        return False, f"pendingMongotGroups={pending}, waiting for {new_shard}"

    run_periodically(check, timeout=120, sleep_time=5, msg="new shard in pendingMongotGroups")
    logger.info(f"New shard {new_shard} confirmed in pendingMongotGroups")


@MARKER
def test_scale_up_envoy_config_has_fallback(namespace: str):
    """While new shard is pending, envoy config should show fallback routing."""
    new_shard = f"{MDB_RESOURCE_NAME}-{SCALED_UP_SHARD_COUNT - 1}"

    def check():
        try:
            lds_config = _get_lds_config(namespace)
            fc = _find_filter_chain_for_shard(lds_config, new_shard)
            if fc is None:
                return False, f"No filter chain found for {new_shard} yet"
            cluster, headers = _get_route_cluster_and_headers(fc)
            if cluster == "mongot_cluster_level_cluster":
                return True, f"Fallback routing active for {new_shard}"
            return False, f"Cluster is {cluster}, waiting for fallback"
        except Exception as e:
            return False, f"Error reading envoy config: {e}"

    run_periodically(check, timeout=120, sleep_time=5, msg="fallback routing for new shard")

    # Full assertion
    lds_config = _get_lds_config(namespace)
    _assert_fallback_routing(lds_config, new_shard)

    # Existing shards should still have normal routing
    for i in range(INITIAL_SHARD_COUNT):
        shard_name = f"{MDB_RESOURCE_NAME}-{i}"
        _assert_normal_routing(lds_config, shard_name)

    logger.info(f"Fallback routing confirmed for {new_shard}, normal for existing shards")


# ===========================================================================
# Phase 4: Once mongot ready, pending cleared and normal routing restored
# ===========================================================================


@MARKER
def test_search_reaches_running_after_scale(namespace: str):
    """Wait for MongoDBSearch to reach Running (all mongot groups ready)."""
    mdbs = _load_mdbs(namespace)
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)
    logger.info("MongoDBSearch reached Running after scale-up")


@MARKER
def test_pending_groups_empty_after_ready(namespace: str):
    """Once all mongots are ready, pendingMongotGroups should be empty."""
    def check():
        pending = _get_pending_mongot_groups(namespace)
        if not pending:
            return True, "pendingMongotGroups is empty"
        return False, f"pendingMongotGroups still has: {pending}"

    run_periodically(check, timeout=120, sleep_time=5, msg="pendingMongotGroups empty")
    logger.info("pendingMongotGroups confirmed empty after all mongots ready")


@MARKER
def test_envoy_config_normal_after_ready(namespace: str):
    """After mongots ready, all shards should have normal per-shard routing."""
    lds_config = _get_lds_config(namespace)
    for i in range(SCALED_UP_SHARD_COUNT):
        shard_name = f"{MDB_RESOURCE_NAME}-{i}"
        _assert_normal_routing(lds_config, shard_name)
        logger.info(f"Shard {shard_name}: normal routing confirmed after ready")
