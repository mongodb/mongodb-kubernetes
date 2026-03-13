"""
E2E test for sharded MongoDB Search with external MongoDB source and operator-managed Envoy LB.

This test is a hybrid of two existing tests:
- search_sharded_enterprise_external_mongod.py (external source, BYO Envoy)
- search_sharded_enterprise_managed_lb.py (internal source, managed LB)

It verifies:
- Deploys a sharded MongoDB cluster with TLS enabled (simulating an external cluster)
- Deploys MongoDBSearch with spec.source.external.shardedCluster + lb.mode: Managed
- Operator automatically deploys and configures Envoy proxy (no manual deploy_envoy_proxy)
- Verifies per-shard mongot Services and StatefulSets
- Verifies mongod/mongos search parameters point to operator-managed Envoy proxy
- Imports sample data, shards collections, creates search indexes
- Executes search queries through mongos and verifies results from all shards

Key difference from BYO LB external test:
- No deploy_envoy_proxy() call - the operator deploys Envoy via lb.mode: Managed
- We verify the operator-created ConfigMap, Deployment, and proxy Services

Key difference from managed LB internal test:
- Uses spec.source.external.shardedCluster (external MongoDB source)
- MDB and MDBS have different resource names (mdb-sh vs mdb-sh-search)
- MongoDB shardOverrides are configured upfront (pointing to operator-managed proxy services)
"""

from kubernetes import client
from pytest import fixture, mark

from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.omtester import skip_if_cloud_manager
from kubetester.phase import Phase
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.prometheus import PrometheusStack
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.search_paging_helper import SearchPagingQueryHelper, run_concurrent_paging_queries
from tests.common.search.sharded_search_helper import *
from tests.conftest import get_default_operator
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

# Prometheus
PROMETHEUS_PASSWORD = "prom-password"
PROMETHEUS_SECRET_NAME = "prometheus-password"

# User credentials
ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"

USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

# Ports
MONGOT_PORT = 27028
ENVOY_PROXY_PORT = 27028
ENVOY_ADMIN_PORT = 9901

# Resource names
MDB_RESOURCE_NAME = "mdb-sh"  # MongoDB resource
MDBS_RESOURCE_NAME = "mdb-sh-search"  # MongoDBSearch resource (different name since external)
SHARD_COUNT = 2
MONGODS_PER_SHARD = 1
MONGOS_COUNT = 1
CONFIG_SERVER_COUNT = 1

# TLS configuration
MDBS_TLS_CERT_PREFIX = "certs"
CA_CONFIGMAP_NAME = "mdb-sh-ca"


@fixture(scope="module")
def sharded_ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    return create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)


@fixture(scope="module")
def prometheus_secret(namespace: str) -> str:
    """Create the secret containing the Prometheus HTTP Basic Auth password."""
    create_or_update_secret(namespace, PROMETHEUS_SECRET_NAME, {"password": PROMETHEUS_PASSWORD})
    return PROMETHEUS_SECRET_NAME


@fixture(scope="function")
def helper(namespace: str) -> SearchDeploymentHelper:
    return SearchDeploymentHelper(
        namespace=namespace,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
    )


@fixture(scope="function")
def mdb(namespace: str, sharded_ca_configmap: str, helper: SearchDeploymentHelper) -> MongoDB:
    return helper.create_sharded_mdb(
        mongot_host_fn=lambda shard: search_resource_names.shard_proxy_service_host(
            MDBS_RESOURCE_NAME, shard, namespace, ENVOY_PROXY_PORT
        ),
        set_tls_ca=True,
    )


@fixture(scope="function")
def mdbs(namespace: str, mdb: MongoDB, helper: SearchDeploymentHelper) -> MongoDBSearch:
    return helper.mdbs_for_ext_sharded_source(
        mongot_user_name=MONGOT_USER_NAME,
        lb_mode="Managed",
        replicas=2,
    )


@fixture(scope="function")
def admin_user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.admin_user_resource(ADMIN_USER_NAME)


@fixture(scope="function")
def user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.user_resource(USER_NAME)


@fixture(scope="function")
def mongot_user(helper: SearchDeploymentHelper, mdbs: MongoDBSearch) -> MongoDBUser:
    return helper.mongot_user_resource(mdbs, MONGOT_USER_NAME)


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    """Test that the operator is installed and running."""
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    """Test OpsManager deployment (skipped for Cloud Manager)."""
    ops_manager = get_ops_manager(namespace)
    assert ops_manager is not None
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_install_tls_certificates(helper: SearchDeploymentHelper, mdb: MongoDB, issuer: str):
    helper.install_sharded_tls_certificates()


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_create_sharded_cluster(mdb: MongoDB):
    """Test sharded cluster deployment."""
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_create_users(
    helper: SearchDeploymentHelper,
    admin_user: MongoDBUser,
    user: MongoDBUser,
    mongot_user: MongoDBUser,
    mdb: MongoDB,
):
    helper.deploy_users(
        admin_user,
        ADMIN_USER_PASSWORD,
        user,
        USER_PASSWORD,
        mongot_user,
        MONGOT_USER_PASSWORD,
    )


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_deploy_lb_certificates(namespace: str, issuer: str):
    """Create TLS certificates for the operator-managed load balancer."""
    create_lb_certificates(namespace, issuer, SHARD_COUNT, MDB_RESOURCE_NAME, MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_create_search_tls_certificate(namespace: str, issuer: str):
    create_per_shard_search_tls_certs(
        namespace, issuer, MDBS_TLS_CERT_PREFIX, SHARD_COUNT, MDB_RESOURCE_NAME, MDBS_RESOURCE_NAME
    )


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_create_search_resource(mdbs: MongoDBSearch):
    """Test MongoDBSearch resource deployment with external sharded source + managed LB."""
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_verify_envoy_deployment(namespace: str):
    envoy_deployment_name = search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME)

    # Verify Envoy Deployment is running (with polling)
    def check_envoy_deployment():
        try:
            apps_v1 = client.AppsV1Api()
            deployment = apps_v1.read_namespaced_deployment(envoy_deployment_name, namespace)
            ready = deployment.status.ready_replicas or 0
            return ready >= 1, f"ready_replicas={ready}"
        except Exception as e:
            return False, f"Deployment {envoy_deployment_name} not found: {e}"

    run_periodically(check_envoy_deployment, timeout=120, sleep_time=5, msg=f"Envoy Deployment {envoy_deployment_name}")
    logger.info(f"Envoy Deployment {envoy_deployment_name} is running")


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_wait_for_sharded_cluster_ready(mdb: MongoDB):
    """Wait for sharded cluster to be ready after Search CR deployment."""
    mdb.assert_reaches_phase(Phase.Running, timeout=600)


# TODO: We don't really need this, it can be removed if we have a way to figure out a logical time
# to wait for to get the mongod/mongos config properly generated.
@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_verify_mongod_parameters_per_shard(namespace: str, mdb: MongoDB, mdbs: MongoDBSearch):
    verify_sharded_mongod_parameters(
        namespace,
        MDB_RESOURCE_NAME,
        mdbs.name,
        SHARD_COUNT,
        expected_host_fn=lambda shard: search_resource_names.shard_proxy_service_host(
            mdbs.name, shard, namespace, ENVOY_PROXY_PORT
        ),
    )


# TODO: We don't really need this, it can be removed if we have a way to figure out a logical time
# to wait for to get the mongod/mongos config properly generated.
@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_verify_mongos_search_config(namespace: str, mdb: MongoDB):
    verify_mongos_search_config(namespace, MDB_RESOURCE_NAME)


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_deploy_tools_pod(tools_pod: mongodb_tools_pod.ToolsPod):
    """Deploy mongodb-tools pod for running queries."""
    logger.info(f"Tools pod {tools_pod.pod_name} is ready")


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_restore_sample_database(mdb: MongoDB, tools_pod: mongodb_tools_pod.ToolsPod):
    search_tester = get_search_tester(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD, use_ssl=True)
    search_tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_shard_collections(mdb: MongoDB):
    search_tester = get_search_tester(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD, use_ssl=True)
    search_tester.shard_and_distribute_collection("sample_mflix", "movies")


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_create_search_index(mdb: MongoDB):
    search_tester = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    search_tester.create_search_index("sample_mflix", "movies")
    search_tester.wait_for_search_indexes_ready("sample_mflix", "movies", timeout=300)


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_execute_text_search_query(mdb: MongoDB):
    search_tester = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    verify_text_search_query(search_tester)


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_verify_search_results_from_all_shards(mdb: MongoDB):
    search_tester = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    verify_search_results_from_all_shards(search_tester)


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_verify_search_resource_status(mdbs: MongoDBSearch):
    """Verify the MongoDBSearch resource is in Running phase with correct status."""
    mdbs.load()

    phase = mdbs.get_status_phase()
    assert phase == Phase.Running, f"MongoDBSearch phase is {phase}, expected Running"
    mdbs.assert_lb_status()

    logger.info(f"MongoDBSearch {mdbs.name} is in Running phase")


def test_paging_query(mdb: MongoDB):
    """Verify that cursor paging (getMore) works by iterating through search results page by page."""
    st = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    helper = SearchPagingQueryHelper(st, "sample_mflix", "movies")
    total = helper.execute_paging_query()
    assert total >= 100, f"Expected at least 100 documents, got {total}"
    logger.info(f"✓ Paging query fetched {total} documents across pages")


def test_concurrent_paging_queries(mdb: MongoDB):
    """Verify cursor paging works under concurrent load from multiple simulated users.

    Each user gets its own SearchTester (and therefore its own connection pool) to avoid
    contention on a shared client. Runs 3 users × 3 iterations in silent mode.
    """
    st = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    num_users = 3
    iterations = 100
    helpers = [
        SearchPagingQueryHelper(st, "sample_mflix", "movies")
        for _ in range(num_users)
    ]
    stats = run_concurrent_paging_queries(helpers, iterations=iterations, silent=True, ignore_errors=False)
    assert stats.total_queries == num_users * iterations, (
        f"Expected {num_users * iterations} queries, got {stats.total_queries}"
    )
    assert stats.total_docs >= num_users * iterations * 100, (
        f"Expected at least {num_users * iterations * 100} total docs, got {stats.total_docs}"
    )
    logger.info(
        f"✓ Concurrent paging: {stats.total_queries} queries, "
        f"{stats.total_pages} pages, {stats.total_docs} docs in {stats.duration_s:.2f}s"
    )


PROMETHEUS_NAMESPACE = "monitoring"


def test_deploy_prometheus_stack(namespace: str):
    """Deploy kube-prometheus-stack with ServiceMonitors for mongod, mongot, and Envoy."""
    stack = PrometheusStack(namespace=PROMETHEUS_NAMESPACE)
    stack.deploy_all(
        target_namespace=namespace,
        mdb_resource_name=MDB_RESOURCE_NAME,
        provision_dashboards=True,
    )
    logger.info(f"✓ Prometheus stack deployed — Grafana: {stack.grafana_url()}")


def test_verify_prometheus_scraping(namespace: str):
    """Verify that Prometheus is actively scraping Envoy targets.

    The Prometheus operator prefixes job labels with the ServiceMonitor/PodMonitor
    namespace: {monitoring_namespace}/{monitor_name}. So the job label for the Envoy
    PodMonitor is "monitoring/mdb-sh-managed-lb-envoy".

    mongod (port 9216) and mongot (port 9946) require spec.prometheus to be enabled
    on the MongoDB and MongoDBSearch CRs respectively — not asserted here until enabled.
    """
    import urllib.request
    import json

    stack = PrometheusStack(namespace=PROMETHEUS_NAMESPACE)

    def _get_targets() -> dict:
        url = f"{stack.prometheus_url()}/api/v1/targets?state=active"
        with urllib.request.urlopen(url, timeout=10) as resp:
            return json.loads(resp.read().decode("utf-8"))

    # Job labels are prefixed with the monitor namespace by the Prometheus operator
    expected_jobs = [
        f"{PROMETHEUS_NAMESPACE}/{MDB_RESOURCE_NAME}-envoy",
    ]

    def _all_jobs_have_targets() -> bool:
        try:
            data = _get_targets()
            active = data.get("data", {}).get("activeTargets", [])
            found_jobs = {t["labels"].get("job") for t in active}
            missing = [j for j in expected_jobs if j not in found_jobs]
            if missing:
                logger.debug(f"Waiting for scrape targets: {missing} (found: {found_jobs})")
                return False
            return True
        except Exception as e:
            logger.debug(f"Prometheus not reachable yet: {e}")
            return False

    run_periodically(
        fn=_all_jobs_have_targets,
        timeout=120,
        sleep_time=10,
        msg="all scrape targets to appear in Prometheus",
    )

    # Assert no targets are permanently down
    data = _get_targets()
    active = data.get("data", {}).get("activeTargets", [])
    for job in expected_jobs:
        job_targets = [t for t in active if t["labels"].get("job") == job]
        assert job_targets, f"No active targets found for job '{job}'"
        down = [t for t in job_targets if t["health"] == "down"]
        assert not down, (
            f"Job '{job}' has {len(down)} down target(s): "
            + "; ".join(t.get("lastError", "unknown") for t in down)
        )
        logger.info(f"✓ Job '{job}': {len(job_targets)} target(s) up")
