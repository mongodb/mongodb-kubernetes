from kubernetes import client
from kubetester import get_service, run_periodically, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.omtester import skip_if_cloud_manager
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.replicaset_search_helper import verify_rs_mongod_parameters
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.search_resource_names import lb_deployment_name, mongot_pod_fqdn, proxy_service_name
from tests.common.search.search_tester import SearchTester
from tests.conftest import get_default_operator, log_deployments_info
from tests.search.om_deployment import get_ops_manager

MDB_RESOURCE_NAME = "mdb-rs"

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = f"{ADMIN_USER_NAME}-password"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = f"{MONGOT_USER_NAME}-password"

USER_NAME = "mdb-user"
USER_PASSWORD = f"{USER_NAME}-password"


# --- Module-scoped fixtures (persist across upgrade) ---


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
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-minimal.yaml"),
        namespace=namespace,
        name=MDB_RESOURCE_NAME,
    )
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
    return helper.mongot_user_resource(mdbs, MONGOT_USER_NAME)


@fixture(scope="module")
def sample_movies_helper(mdb: MongoDB, namespace: str) -> SampleMoviesSearchHelper:
    return SampleMoviesSearchHelper(
        SearchTester.for_replicaset(mdb, USER_NAME, USER_PASSWORD),
        tools_pod=mongodb_tools_pod.get_tools_pod(namespace),
    )


@mark.e2e_operator_upgrade_search
class TestDeployOnOfficialOperator:

    def test_install_latest_official_operator(self, namespace: str, official_operator: Operator):
        official_operator.assert_is_running()
        log_deployments_info(namespace)

    @skip_if_cloud_manager
    def test_create_ops_manager(self, namespace: str):
        ops_manager = get_ops_manager(namespace)
        assert ops_manager is not None
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_create_database_resource(self, mdb: MongoDB):
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
        helper.deploy_users(
            admin_user,
            ADMIN_USER_PASSWORD,
            user,
            USER_PASSWORD,
            mongot_user,
            MONGOT_USER_PASSWORD,
        )

    def test_create_search_resource(self, mdbs: MongoDBSearch):
        mdbs.update()
        mdbs.assert_reaches_phase(Phase.Running, timeout=300)

    def test_wait_for_database_resource_ready(self, mdb: MongoDB):
        mdb.assert_reaches_phase(Phase.Running, timeout=300)

    def test_restore_sample_database(self, sample_movies_helper: SampleMoviesSearchHelper):
        sample_movies_helper.restore_sample_database()

    def test_verify_mongod_search_params(self, namespace: str, mdb: MongoDB):
        expected_host = mongot_pod_fqdn(MDB_RESOURCE_NAME, namespace, 27028)
        verify_rs_mongod_parameters(namespace, MDB_RESOURCE_NAME, mdb.get_members(), expected_host)

    def test_create_search_index(self, sample_movies_helper: SampleMoviesSearchHelper):
        sample_movies_helper.create_search_index()

    def test_search_query_before_upgrade(self, sample_movies_helper: SampleMoviesSearchHelper):
        sample_movies_helper.assert_search_query(retry_timeout=60)


@mark.e2e_operator_upgrade_search
class TestOperatorUpgrade:

    def test_upgrade_operator(self, namespace: str, operator_installation_config: dict[str, str]):
        operator = get_default_operator(
            namespace, operator_installation_config=operator_installation_config, apply_crds_first=True
        )
        operator.assert_is_running()
        log_deployments_info(namespace)

    def test_search_running_after_upgrade(self, mdbs: MongoDBSearch):
        mdbs.assert_reaches_phase(phase=Phase.Running, timeout=300)

    def test_database_running_after_upgrade(self, mdb: MongoDB):
        mdb.assert_reaches_phase(phase=Phase.Running, timeout=300)

    def test_search_query_after_upgrade(self, sample_movies_helper: SampleMoviesSearchHelper):
        sample_movies_helper.assert_search_query(retry_timeout=60)


@mark.e2e_operator_upgrade_search
class TestScaleWithManagedLB:

    def test_enable_multi_mongot_and_managed_lb(self, mdbs: MongoDBSearch):
        mdbs.load()
        mdbs["spec"]["replicas"] = 2
        mdbs["spec"]["loadBalancer"] = {"managed": {}}
        mdbs.update()

    def test_search_running_after_scale(self, mdbs: MongoDBSearch):
        mdbs.assert_reaches_phase(phase=Phase.Running, timeout=600)

    def test_verify_lb_status(self, mdbs: MongoDBSearch):
        mdbs.load()
        mdbs.assert_lb_status()

    def test_verify_envoy_deployment(self, namespace: str):
        envoy_name = lb_deployment_name(MDB_RESOURCE_NAME)

        def check_envoy_ready():
            try:
                deployment = client.AppsV1Api().read_namespaced_deployment(envoy_name, namespace)
                ready = deployment.status.ready_replicas or 0
                return ready >= 1, f"ready_replicas={ready}"
            except Exception as e:
                return False, f"Deployment {envoy_name} not found: {e}"

        run_periodically(check_envoy_ready, timeout=120, sleep_time=5, msg=f"Envoy Deployment {envoy_name}")

    def test_verify_proxy_service(self, namespace: str):
        svc_name = proxy_service_name(MDB_RESOURCE_NAME)
        svc = get_service(namespace, svc_name)
        assert svc is not None, f"Proxy service {svc_name} not found"

    def test_search_query_after_scale(self, sample_movies_helper: SampleMoviesSearchHelper):
        sample_movies_helper.assert_search_query(retry_timeout=60)
