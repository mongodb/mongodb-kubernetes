"""Search availability across a released -> dev operator upgrade, on a managed-LB deployment.

The "from" state is deployed on the latest released operator using the released CR API
(``spec.replicas`` + ``loadBalancer.managed: {}``) — both fields exist in the released and dev CRDs,
so the resource is valid before and after the CRD migration, and the dev operator auto-promotes the
deprecated ``spec.replicas`` to ``clusters[].replicas``. This is a real operator-version transition
(new reconciler + CRD migration), unlike a same-binary image bump.

Topology: 2 mongot behind 1 managed Envoy (``loadBalancer.managed.replicas`` is dev-only, so the
released "from" state has a single envoy).

Paths:
  - operator-upgrade: helm-upgrade released -> dev. mongot rolls one at a time and the surviving
    replica serves through it (ride-through); a separate check confirms the mongot image actually
    changed. Disruption is bounded.
  - envoy-scale: post-upgrade, set ``managed.replicas=2`` — a field the released CRD lacked. Envoy
    scales 1 -> 2 (endpoint added, not rolled), proving the migration unlocked the field while
    availability holds. Multi-envoy ride-through lives in search_availability_upgrade_dataplane.py.

EVG-only: the upgrade reconciles via an in-cluster operator, incompatible with local `make run`.
"""

from __future__ import annotations

from kubernetes import client
from kubetester import get_service, get_statefulset, run_periodically, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.omtester import skip_if_cloud_manager
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark, skip
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search.connectivity import SearchConnectivityTool
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.rs_search_helper import rs_search_tester
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.search_resource_names import lb_deployment_name, mongot_statefulset_name, proxy_service_name
from tests.common.search.search_tester import SearchTester
from tests.common.search.upgrade_availability import run_upgrade_availability
from tests.conftest import get_default_operator, local_operator, log_deployments_info
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

MDB_RESOURCE_NAME = "mdb-rs-avail-up"

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = f"{ADMIN_USER_NAME}-password"
MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = f"{MONGOT_USER_NAME}-password"
USER_NAME = "mdb-user"
USER_PASSWORD = f"{USER_NAME}-password"

MONGOT_REPLICAS = 2
ENVOY_REPLICAS_AFTER_SCALE = 2
# Sustained-outage ceiling; the surviving mongot rides through a one-at-a-time roll, so the real value
# is small. Fails a pathological run.
DISRUPTION_BOUND_S = 60.0

pytestmark = mark.skipif(local_operator(), reason="operator-version upgrade needs an in-cluster operator (EVG-only)")


def user_connectivity_tool(namespace: str) -> SearchConnectivityTool:
    # The mongod RS is deployed without TLS, so connect without TLS to match it.
    return SearchConnectivityTool(
        rs_search_tester(MDB_RESOURCE_NAME, namespace, USER_NAME, USER_PASSWORD, use_ssl=False)
    )


def get_mongot_image_tag(namespace: str) -> str:
    """The released operator names the StatefulSet ``{name}-search``; the dev operator uses the
    cluster-indexed name. Try both so this reads before and after the upgrade."""
    for name in (mongot_statefulset_name(MDB_RESOURCE_NAME), f"{MDB_RESOURCE_NAME}-search"):
        try:
            sts = get_statefulset(namespace, name)
        except Exception:
            continue
        mongot = next((c for c in sts.spec.template.spec.containers if c.name == "mongot"), None)
        if mongot:
            return mongot.image.split(":")[-1]
    raise AssertionError("mongot StatefulSet not found under either naming convention")


def envoy_ready_replicas(namespace: str) -> int:
    dep = client.AppsV1Api().read_namespaced_deployment(lb_deployment_name(MDB_RESOURCE_NAME), namespace)
    return dep.status.ready_replicas or 0


# --- Module-scoped fixtures (persist across the upgrade) ---


@fixture(scope="module")
def helper(namespace: str) -> SearchDeploymentHelper:
    return SearchDeploymentHelper(
        namespace=namespace,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDB_RESOURCE_NAME,
    )


@fixture(scope="module")
def mdb(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("enterprise-replicaset-sample-mflix.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )
    resource.configure(om=get_ops_manager(namespace), project_name=MDB_RESOURCE_NAME)
    try_load(resource)
    return resource


@fixture(scope="module")
def mdbs(namespace: str) -> MongoDBSearch:
    """Multi-mongot managed LB via the released CR API: spec.replicas + loadBalancer.managed."""
    resource = MongoDBSearch.from_yaml(yaml_fixture("search-minimal.yaml"), namespace=namespace, name=MDB_RESOURCE_NAME)
    resource["spec"]["replicas"] = MONGOT_REPLICAS
    resource["spec"]["loadBalancer"] = {"managed": {}}
    try_load(resource)
    return resource


@fixture(scope="module")
def admin_user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.admin_user_resource(ADMIN_USER_NAME)


@fixture(scope="module")
def user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.user_resource(USER_NAME)


@fixture(scope="module")
def mongot_user(helper: SearchDeploymentHelper, mdbs: MongoDBSearch) -> MongoDBUser:
    return helper.mongot_user_resource(mdbs.name, MONGOT_USER_NAME)


@fixture(scope="module")
def sample_movies_helper(mdb: MongoDB, namespace: str) -> SampleMoviesSearchHelper:
    return SampleMoviesSearchHelper(
        SearchTester.for_replicaset(mdb, USER_NAME, USER_PASSWORD),
        tools_pod=mongodb_tools_pod.get_tools_pod(namespace),
    )


@mark.e2e_search_availability_upgrade_operator
class TestDeployOnOfficialOperator:
    def test_install_official_operator(self, namespace: str, official_operator: Operator):
        official_operator.assert_is_running()
        log_deployments_info(namespace)

    @skip_if_cloud_manager
    def test_create_ops_manager(self, namespace: str):
        ops_manager = get_ops_manager(namespace)
        assert ops_manager is not None
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_create_database(self, mdb: MongoDB):
        mdb.update()
        mdb.assert_reaches_phase(Phase.Running, timeout=300)

    def test_create_users(
        self,
        helper: SearchDeploymentHelper,
        admin_user: MongoDBUser,
        user: MongoDBUser,
        mongot_user: MongoDBUser,
        mdb: MongoDB,
    ):
        helper.deploy_users(admin_user, ADMIN_USER_PASSWORD, user, USER_PASSWORD, mongot_user, MONGOT_USER_PASSWORD)

    def test_create_search(self, mdbs: MongoDBSearch):
        mdbs.update()
        mdbs.assert_reaches_phase(Phase.Running, timeout=300)

    def test_database_ready_after_search(self, mdb: MongoDB):
        mdb.assert_abandons_phase(Phase.Running, timeout=300)
        mdb.assert_reaches_phase(Phase.Running, timeout=300)

    def test_restore_sample_database(self, sample_movies_helper: SampleMoviesSearchHelper):
        sample_movies_helper.restore_sample_database()

    def test_create_search_index(self, sample_movies_helper: SampleMoviesSearchHelper):
        sample_movies_helper.create_search_index()

    def test_search_query_before_upgrade(self, sample_movies_helper: SampleMoviesSearchHelper):
        sample_movies_helper.assert_search_query(retry_timeout=60)


@mark.e2e_search_availability_upgrade_operator
class TestOperatorUpgrade:
    def test_upgrade_availability(
        self, namespace: str, operator_installation_config: dict[str, str], mdbs: MongoDBSearch
    ):
        """Helm-upgrade released -> dev. mongot rolls and the open cursor rides through."""
        tag_before = get_mongot_image_tag(namespace)

        def apply_upgrade() -> None:
            get_default_operator(
                namespace, operator_installation_config=operator_installation_config, apply_crds_first=True
            ).assert_is_running()
            mdbs.assert_reaches_phase(Phase.Running, timeout=600)

        run_upgrade_availability(
            namespace,
            tool_factory=lambda: user_connectivity_tool(namespace),
            apply_upgrade=apply_upgrade,
            path="operator_upgrade",
            disruption_bound_s=DISRUPTION_BOUND_S,
        )
        tag_after = get_mongot_image_tag(namespace)
        assert tag_after != tag_before, f"operator upgrade was a no-op: mongot tag stayed {tag_before}"
        log_deployments_info(namespace)

    def test_search_query_after_upgrade(self, sample_movies_helper: SampleMoviesSearchHelper):
        sample_movies_helper.assert_search_query(retry_timeout=60)


@mark.e2e_search_availability_upgrade_operator
class TestEnvoyScaleAfterUpgrade:
    def test_envoy_scale_availability(self, namespace: str, mdbs: MongoDBSearch):
        """managed.replicas (dev-only) is now usable post-migration: scale envoy 1 -> 2 while the
        background load runs. The endpoint is added without rolling the existing envoy, so this
        asserts the field took effect and availability held — not a roll."""
        ready_before = envoy_ready_replicas(namespace)
        if ready_before >= ENVOY_REPLICAS_AFTER_SCALE:
            skip(f"envoy already at {ready_before} replicas; nothing to scale")

        def envoy_scaled() -> tuple[bool, str]:
            ready = envoy_ready_replicas(namespace)
            return ready >= ENVOY_REPLICAS_AFTER_SCALE, f"ready={ready}"

        def apply_scale() -> None:
            mdbs.load()
            mdbs["spec"]["loadBalancer"]["managed"]["replicas"] = ENVOY_REPLICAS_AFTER_SCALE
            mdbs.update()
            mdbs.assert_reaches_phase(Phase.Running, timeout=600)
            run_periodically(envoy_scaled, timeout=300, sleep_time=5, msg=f"envoy -> {ENVOY_REPLICAS_AFTER_SCALE}")

        run_upgrade_availability(
            namespace,
            tool_factory=lambda: user_connectivity_tool(namespace),
            apply_upgrade=apply_scale,
            path="envoy_scale_up",
            disruption_bound_s=DISRUPTION_BOUND_S,
        )
        assert envoy_ready_replicas(namespace) >= ENVOY_REPLICAS_AFTER_SCALE

    def test_verify_lb_status(self, mdbs: MongoDBSearch):
        mdbs.load()
        mdbs.assert_lb_status()

    def test_verify_proxy_service(self, namespace: str):
        svc = get_service(namespace, proxy_service_name(MDB_RESOURCE_NAME))
        assert svc is not None

    def test_search_query_after_scale(self, sample_movies_helper: SampleMoviesSearchHelper):
        sample_movies_helper.assert_search_query(retry_timeout=60)
