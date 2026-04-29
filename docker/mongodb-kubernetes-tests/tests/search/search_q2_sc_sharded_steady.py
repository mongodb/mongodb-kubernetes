"""
Q2-MC MongoDBSearch e2e scaffold: single-cluster, sharded source, NEW shape.

Exercises the new `spec.clusters: [{...}]` distribution shape on a *single*
cluster against a sharded MongoDB source (router + 2 shards × 1 cluster).
The legacy single-cluster sharded path remains covered by
`search_sharded_enterprise_external_mongod_managed_lb`.

Verifies (when the full stack converges):
- A 2-shard Enterprise sharded cluster with TLS deploys, with mongotHost
  per shard pointed at the corresponding managed Envoy proxy
- A MongoDBSearch with `spec.source.external.shardedCluster.{router,shards[]}`,
  `spec.clusters[0]` (replicas=2, syncSourceSelector), and
  `spec.loadBalancer.managed.externalHostname:
    "{clusterName}-{shardName}.search-lb.example.com:443"` reaches Phase=Running
- `status.clusterStatusList.clusterStatuses[0].phase == "Running"`
- $search query through mongos returns results from all shards

NOTE: parts of the stack are still being landed (per-cluster Envoy: PR #1036;
sharded source matrix: PR #1032; clusterStatusList: TBD). The scaffold
compiles and collects; runtime steps will pass once the stack converges.
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
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.sharded_search_helper import (
    create_issuer_ca,
    create_lb_certificates,
    create_per_shard_search_tls_certs,
    get_search_tester,
    verify_search_results_from_all_shards,
    verify_text_search_query,
)
from tests.conftest import get_default_operator
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"
MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"
USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

ENVOY_PROXY_PORT = 27028

MDB_RESOURCE_NAME = "mdb-sh-q2-sc"
MDBS_RESOURCE_NAME = "mdb-sh-q2-sc-search"
SHARD_COUNT = 2
MONGODS_PER_SHARD = 1
MONGOS_COUNT = 1

MDBS_TLS_CERT_PREFIX = "certs"
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"

SINGLE_CLUSTER_NAME = "kind-e2e-cluster-1"
SINGLE_REGION_TAG = "us-east"


@fixture(scope="module")
def sharded_ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    return create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)


@fixture(scope="function")
def helper(namespace: str) -> SearchDeploymentHelper:
    return SearchDeploymentHelper(
        namespace=namespace,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        shard_count=SHARD_COUNT,
        mongods_per_shard=MONGODS_PER_SHARD,
        mongos_count=MONGOS_COUNT,
    )


@fixture(scope="function")
def mdb(namespace: str, sharded_ca_configmap: str, helper: SearchDeploymentHelper) -> MongoDB:
    resource = helper.create_sharded_mdb(
        mongot_host_fn=lambda shard: search_resource_names.shard_proxy_service_host(
            MDBS_RESOURCE_NAME, shard, namespace, ENVOY_PROXY_PORT
        ),
        set_tls_ca=True,
    )
    resource["spec"]["mongosCount"] = MONGOS_COUNT
    return resource


@fixture(scope="function")
def mdbs(namespace: str) -> MongoDBSearch:
    """Loads new-shape sharded fixture and patches in real router/shard hosts and clusters[0]."""
    resource = MongoDBSearch.from_yaml(
        find_fixture("search-q2-sc-sharded.yaml"),
        namespace=namespace,
        name=MDBS_RESOURCE_NAME,
    )

    router_hosts = [
        f"{MDB_RESOURCE_NAME}-mongos-{i}.{MDB_RESOURCE_NAME}-svc.{namespace}.svc.cluster.local:27017"
        for i in range(MONGOS_COUNT)
    ]
    shards = []
    for shard_idx in range(SHARD_COUNT):
        shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
        shard_hosts = [
            f"{shard_name}-{m}.{MDB_RESOURCE_NAME}-sh.{namespace}.svc.cluster.local:27017"
            for m in range(MONGODS_PER_SHARD)
        ]
        shards.append({"shardName": shard_name, "hosts": shard_hosts})

    resource["spec"]["source"] = {
        "username": MONGOT_USER_NAME,
        "passwordSecretRef": {
            "name": f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password",
            "key": "password",
        },
        "external": {
            "shardedCluster": {
                "router": {"hosts": router_hosts},
                "shards": shards,
            },
            "tls": {"ca": {"name": CA_CONFIGMAP_NAME}},
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


@mark.e2e_search_q2_sc_sharded_steady
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_q2_sc_sharded_steady
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    ops_manager = get_ops_manager(namespace)
    assert ops_manager is not None
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_q2_sc_sharded_steady
def test_install_tls_certificates(helper: SearchDeploymentHelper, mdb: MongoDB, issuer: str):
    helper.install_sharded_tls_certificates()


@mark.e2e_search_q2_sc_sharded_steady
def test_create_sharded_cluster(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_search_q2_sc_sharded_steady
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


@mark.e2e_search_q2_sc_sharded_steady
def test_deploy_lb_certificates(namespace: str, issuer: str):
    create_lb_certificates(namespace, issuer, SHARD_COUNT, MDB_RESOURCE_NAME, MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)


@mark.e2e_search_q2_sc_sharded_steady
def test_create_search_tls_certificate(namespace: str, issuer: str):
    create_per_shard_search_tls_certs(
        namespace, issuer, MDBS_TLS_CERT_PREFIX, SHARD_COUNT, MDB_RESOURCE_NAME, MDBS_RESOURCE_NAME
    )


@mark.e2e_search_q2_sc_sharded_steady
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_q2_sc_sharded_steady
def test_verify_per_cluster_status(mdbs: MongoDBSearch):
    """Per-cluster + per-shard status assertions.

    Raw dict access — status.clusterStatusList isn't on the Go status struct
    yet (lands with the per-cluster status PR). Once it lands, this fires.
    """
    mdbs.load()
    cluster_statuses = mdbs["status"]["clusterStatusList"]["clusterStatuses"]
    assert len(cluster_statuses) == 1, f"expected 1 cluster status entry, got {len(cluster_statuses)}"

    cs = cluster_statuses[0]
    assert cs["phase"] == "Running", f"clusterStatuses[0].phase={cs['phase']}, expected Running"

    shard_lb = cs["loadBalancer"]["shards"]
    for shard_idx in range(SHARD_COUNT):
        shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
        assert shard_name in shard_lb, f"missing shard status for {shard_name}"
        phase = shard_lb[shard_name]["phase"]
        assert phase == "Running", f"shards[{shard_name}].phase={phase}, expected Running"


@mark.e2e_search_q2_sc_sharded_steady
def test_verify_lb_status(mdbs: MongoDBSearch):
    mdbs.load()
    mdbs.assert_lb_status()


@mark.e2e_search_q2_sc_sharded_steady
def test_deploy_tools_pod(tools_pod: mongodb_tools_pod.ToolsPod):
    logger.info(f"Tools pod {tools_pod.pod_name} is ready")


@mark.e2e_search_q2_sc_sharded_steady
def test_restore_sample_database(mdb: MongoDB, tools_pod: mongodb_tools_pod.ToolsPod):
    search_tester = get_search_tester(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD, use_ssl=True)
    search_tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )


@mark.e2e_search_q2_sc_sharded_steady
def test_create_search_index(mdb: MongoDB):
    search_tester = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    search_tester.create_search_index("sample_mflix", "movies")
    search_tester.wait_for_search_indexes_ready("sample_mflix", "movies", timeout=300)


@mark.e2e_search_q2_sc_sharded_steady
def test_execute_text_search_query(mdb: MongoDB):
    search_tester = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    verify_text_search_query(search_tester)


@mark.e2e_search_q2_sc_sharded_steady
def test_execute_all_shards_search(mdb: MongoDB):
    search_tester = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    verify_search_results_from_all_shards(search_tester)
