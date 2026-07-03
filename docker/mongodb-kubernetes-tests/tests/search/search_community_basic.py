import yaml
from kubernetes import client
from kubetester import create_or_update_secret, read_configmap, statefulset_is_deleted, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb_community import MongoDBCommunity
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import movies_search_helper, search_resource_names
from tests.common.search.connectivity import wait_for_mongot_pvcs_deleted, wait_for_mongot_statefulset_drained
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_tester import SearchTester
from tests.conftest import get_default_operator

logger = test_logger.get_test_logger(__name__)

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"

USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

MDBC_RESOURCE_NAME = "mdbc-rs"


@fixture(scope="function")
def mdbc(namespace: str) -> MongoDBCommunity:
    resource = MongoDBCommunity.from_yaml(
        yaml_fixture("community-replicaset-sample-mflix.yaml"),
        name=MDBC_RESOURCE_NAME,
        namespace=namespace,
    )

    try_load(resource)
    return resource


ADVANCED_MONGOT_CONFIGS = {"indexing": {"lucene": {"fieldLimit": 1500}}}


@fixture(scope="function")
def mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-minimal.yaml"),
        namespace=namespace,
    )

    try_load(resource)
    resource["spec"]["clusters"][0]["advancedMongotConfigs"] = ADVANCED_MONGOT_CONFIGS
    return resource


@mark.e2e_search_community_basic
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.wait_for_operator_ready()


@mark.e2e_search_community_basic
def test_install_secrets(namespace: str, mdbs: MongoDBSearch):
    create_or_update_secret(namespace=namespace, name=f"{USER_NAME}-password", data={"password": USER_PASSWORD})
    create_or_update_secret(
        namespace=namespace, name=f"{ADMIN_USER_NAME}-password", data={"password": ADMIN_USER_PASSWORD}
    )
    create_or_update_secret(
        namespace=namespace, name=f"{mdbs.name}-{MONGOT_USER_NAME}-password", data={"password": MONGOT_USER_PASSWORD}
    )


@mark.e2e_search_community_basic
def test_create_database_resource(mdbc: MongoDBCommunity):
    mdbc.update()
    mdbc.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_community_basic
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_community_basic
def test_advanced_mongot_configs_rendered(namespace: str, mdbs: MongoDBSearch):
    data = read_configmap(namespace, search_resource_names.mongot_configmap_name(mdbs.name))
    config = yaml.safe_load(data["config.yml"])
    assert config["advancedConfigs"] == ADVANCED_MONGOT_CONFIGS, "block must be rendered verbatim under its key"
    assert config["storage"]["dataPath"], "operator-rendered settings must be unaffected"


@mark.e2e_search_community_basic
def test_wait_for_community_resource_ready(mdbc: MongoDBCommunity):
    mdbc.assert_reaches_phase(Phase.Running, timeout=600)


@fixture(scope="function")
def sample_movies_helper(mdbc: MongoDBCommunity, namespace: str) -> SampleMoviesSearchHelper:
    return movies_search_helper.SampleMoviesSearchHelper(
        SearchTester.for_replicaset(mdbc, USER_NAME, USER_PASSWORD),
        tools_pod=mongodb_tools_pod.get_tools_pod(namespace),
    )


@mark.e2e_search_community_basic
def test_search_restore_sample_database(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.restore_sample_database()


@mark.e2e_search_community_basic
def test_search_create_search_index(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.create_search_index()


@mark.e2e_search_community_basic
def test_search_assert_search_query(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.assert_search_query(retry_timeout=60)


def _mongot_data_pvc_names(namespace: str, sts_name: str) -> list[str]:
    """Names of the data PVCs backing a mongot StatefulSet (volumeClaimTemplate
    ``data`` -> ``data-<sts>-<ordinal>``)."""
    core_v1 = client.CoreV1Api()
    prefix = f"data-{sts_name}-"
    return [
        pvc.metadata.name
        for pvc in core_v1.list_namespaced_persistent_volume_claim(namespace).items
        if pvc.metadata.name.startswith(prefix)
    ]


@mark.e2e_search_community_basic
def test_scale_mongot_offline_reclaims_pvc(namespace: str, mdbs: MongoDBSearch):
    """whenScaled:Delete - scaling mongot to 0 keeps the CR and StatefulSet object
    but reclaims the orphaned ordinal's data PVC; restoring to 1 reindexes from
    mongod onto a fresh volume."""
    sts_name = search_resource_names.mongot_statefulset_name(mdbs.name)

    # Presence guard: the STS and its data PVC must exist before scale-down, else
    # the reclaim assertion below passes vacuously.
    client.AppsV1Api().read_namespaced_stateful_set(sts_name, namespace)
    assert _mongot_data_pvc_names(namespace, sts_name), f"expected a mongot data PVC for {sts_name} before scale-down"

    mdbs.load()
    mdbs["spec"]["clusters"][0]["replicas"] = 0
    mdbs.update()

    wait_for_mongot_statefulset_drained(sts_name, namespace, timeout=300)
    # Block on the delete completing before scaling back up to dodge the
    # scale-up-while-Terminating race (see wait_for_mongot_pvcs_deleted).
    wait_for_mongot_pvcs_deleted(namespace, sts_name, timeout=300)

    mdbs.load()
    mdbs["spec"]["clusters"][0]["replicas"] = 1
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_community_basic
def test_delete_search_cr_reclaims_pvc(namespace: str, mdbs: MongoDBSearch):
    """On MongoDBSearch CR delete (single-cluster), owner-ref GC removes the mongot
    StatefulSet and the persistentVolumeClaimRetentionPolicy{whenDeleted:Delete}
    reclaims its data PVC."""
    sts_name = search_resource_names.mongot_statefulset_name(mdbs.name)

    # Presence guard: the STS and its data PVC must exist before delete, otherwise
    # the post-delete absence checks below could pass vacuously.
    client.AppsV1Api().read_namespaced_stateful_set(sts_name, namespace)
    pvcs_before = _mongot_data_pvc_names(namespace, sts_name)
    assert pvcs_before, f"expected at least one mongot data PVC for {sts_name} before delete"
    logger.info(f"pre-delete: STS {sts_name} present, data PVCs {pvcs_before}")

    mdbs.delete()

    run_periodically(
        lambda: statefulset_is_deleted(namespace, sts_name, api_client=None),
        timeout=300,
        sleep_time=5,
        msg=f"mongot StatefulSet {sts_name} deletion after MongoDBSearch delete",
    )
    wait_for_mongot_pvcs_deleted(namespace, sts_name, timeout=300)
