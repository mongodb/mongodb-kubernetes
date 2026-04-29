"""
Q2-MC MongoDBSearch e2e scaffold: single-cluster, ReplicaSet source, NEW shape.

Exercises the new `spec.clusters: [{...}]` distribution shape on a *single*
cluster. The legacy single-cluster path (top-level spec.replicas, no
spec.clusters) is covered by the existing
`search_replicaset_external_mongodb_multi_mongot_managed_lb` test and
remains unchanged for backward-compat coverage.

What this test verifies (when the full stack converges):
- Enterprise RS deploys with mongotHost pointed at managed Envoy
- A MongoDBSearch with `spec.clusters[0]` (replicas=2, syncSourceSelector) and
  `spec.loadBalancer.managed.externalHostname: "{clusterName}.search-lb.example.com:443"`
  reaches Phase=Running
- `status.clusterStatusList.clusterStatuses[0].phase == "Running"`
- Sample mflix data is restored, a search index is created, and a $search
  query returns results
"""

from kubetester import find_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.omtester import skip_if_cloud_manager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import search_resource_names
from tests.common.search.q2_shared import (
    ADMIN_USER_NAME,
    ADMIN_USER_PASSWORD,
    ENVOY_PROXY_PORT,
    MDBS_TLS_CERT_PREFIX,
    MONGOT_USER_NAME,
    MONGOT_USER_PASSWORD,
    USER_NAME,
    USER_PASSWORD,
    q2_create_search_index,
    q2_restore_sample,
    q2_text_search_query,
)
from tests.common.search.q2_topology import SINGLE_CLUSTER_NAME, SINGLE_REGION_TAG
from tests.common.search.rs_search_helper import (
    create_rs_lb_certificates,
    create_rs_search_tls_cert,
    get_rs_search_tester,
    verify_rs_mongod_parameters,
)
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.sharded_search_helper import create_issuer_ca
from tests.conftest import get_default_operator
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

MDB_RESOURCE_NAME = "mdb-rs-q2-sc"
MDBS_RESOURCE_NAME = "mdb-rs-q2-sc-search"
RS_MEMBERS = 3

CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"


@fixture(scope="module")
def ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    return create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)


@fixture(scope="function")
def helper(namespace: str) -> SearchDeploymentHelper:
    return SearchDeploymentHelper(
        namespace=namespace,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        ca_configmap_name=CA_CONFIGMAP_NAME,
    )


@fixture(scope="function")
def mdb(namespace: str, ca_configmap: str, helper: SearchDeploymentHelper) -> MongoDB:
    """RS pre-configured with mongotHost pointed at managed Envoy."""
    proxy_host = search_resource_names.proxy_service_host(MDBS_RESOURCE_NAME, namespace, ENVOY_PROXY_PORT)
    return helper.create_rs_mdb(set_tls=True, mongot_host=proxy_host)


@fixture(scope="function")
def mdbs(namespace: str) -> MongoDBSearch:
    """MongoDBSearch loaded from the new-shape fixture; clusters[0] patched in-place."""
    resource = MongoDBSearch.from_yaml(
        find_fixture("search-q2-sc-rs.yaml"),
        namespace=namespace,
        name=MDBS_RESOURCE_NAME,
    )
    resource["spec"]["source"] = {
        "mongodbResourceRef": {"name": MDB_RESOURCE_NAME},
        "username": MONGOT_USER_NAME,
        "passwordSecretRef": {
            "name": f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password",
            "key": "password",
        },
    }
    resource["spec"]["clusters"] = [
        {
            "clusterName": SINGLE_CLUSTER_NAME,
            "replicas": 2,
            "syncSourceSelector": {"matchTags": {"region": SINGLE_REGION_TAG}},
        }
    ]
    return resource


@fixture(scope="function")
def admin_user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.admin_user_resource(ADMIN_USER_NAME)


@fixture(scope="function")
def user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.user_resource(USER_NAME)


@fixture(scope="function")
def mongot_user(helper: SearchDeploymentHelper, mdbs: MongoDBSearch) -> MongoDBUser:
    return helper.mongot_user_resource(mdbs, MONGOT_USER_NAME)


@mark.e2e_search_q2_sc_rs_steady
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_q2_sc_rs_steady
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    ops_manager = get_ops_manager(namespace)
    assert ops_manager is not None
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_q2_sc_rs_steady
def test_install_tls_certificates(helper: SearchDeploymentHelper, mdb: MongoDB, issuer: str):
    helper.install_rs_tls_certificates(issuer, members=RS_MEMBERS)


@mark.e2e_search_q2_sc_rs_steady
def test_create_database_resource(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_q2_sc_rs_steady
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


@mark.e2e_search_q2_sc_rs_steady
def test_deploy_lb_certificates(namespace: str, issuer: str):
    create_rs_lb_certificates(namespace, issuer, MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)


@mark.e2e_search_q2_sc_rs_steady
def test_create_search_tls_certificate(namespace: str, issuer: str):
    create_rs_search_tls_cert(namespace, issuer, MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)


@mark.e2e_search_q2_sc_rs_steady
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_q2_sc_rs_steady
def test_verify_per_cluster_status(mdbs: MongoDBSearch):
    """Assert the new clusterStatusList per-cluster phase is Running."""
    mdbs.load()
    cluster_statuses = mdbs["status"]["clusterStatusList"]["clusterStatuses"]
    assert len(cluster_statuses) == 1, f"expected 1 cluster status entry, got {len(cluster_statuses)}"
    phase = cluster_statuses[0]["phase"]
    assert phase == "Running", f"clusterStatuses[0].phase={phase}, expected Running"


@mark.e2e_search_q2_sc_rs_steady
def test_verify_lb_status(mdbs: MongoDBSearch):
    mdbs.load()
    mdbs.assert_lb_status()


@mark.e2e_search_q2_sc_rs_steady
def test_verify_mongod_parameters(namespace: str, mdb: MongoDB, mdbs: MongoDBSearch):
    expected_host = search_resource_names.proxy_service_host(mdbs.name, namespace, ENVOY_PROXY_PORT)
    verify_rs_mongod_parameters(namespace, MDB_RESOURCE_NAME, RS_MEMBERS, expected_host)


@mark.e2e_search_q2_sc_rs_steady
def test_deploy_tools_pod(tools_pod: mongodb_tools_pod.ToolsPod):
    logger.info(f"Tools pod {tools_pod.pod_name} is ready")


@mark.e2e_search_q2_sc_rs_steady
def test_restore_sample_database(mdb: MongoDB, tools_pod: mongodb_tools_pod.ToolsPod):
    q2_restore_sample(mdb, tools_pod, get_rs_search_tester)


@mark.e2e_search_q2_sc_rs_steady
def test_create_search_index(mdb: MongoDB):
    q2_create_search_index(mdb, get_rs_search_tester)


@mark.e2e_search_q2_sc_rs_steady
def test_execute_text_search_query(mdb: MongoDB):
    q2_text_search_query(mdb, get_rs_search_tester)
