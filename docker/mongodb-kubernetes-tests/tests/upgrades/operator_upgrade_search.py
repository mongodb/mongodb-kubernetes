import json
from copy import deepcopy
from typing import Any

from kubernetes import client
from kubetester import run_periodically, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from kubetester.omtester import skip_if_cloud_manager
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import search_resource_names
from tests.common.search.connectivity import mongot_data_pvc_names
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_deployment_helper import (
    SearchDeploymentHelper,
    ensure_search_issuer,
)
from tests.common.search.sharded_search_helper import (
    create_issuer_ca,
    create_lb_certificates,
    create_per_shard_search_tls_certs,
    get_search_tester,
)
from tests.conftest import (
    get_default_operator,
    install_official_operator,
    log_deployments_info,
)
from tests.constants import MCK_HELM_CHART, OFFICIAL_OPERATOR_IMAGE_NAME, OPERATOR_NAME
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

GA_OPERATOR_VERSION = "1.9.0"
SHARD_COUNT = 2
MONGODS_PER_SHARD = 1
MONGOS_COUNT = 1
CONFIG_SERVER_COUNT = 1

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"
USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"
MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"

MDB_RESOURCE_NAME = "mdb-upgrade-sh"
MDBS_RESOURCE_NAME = "mdb-upgrade-search"
MDBS_TLS_CERT_PREFIX = "certs"
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"

SEARCH_NAME_LABEL = "mongodb.com/search-name"
SEARCH_NAMESPACE_LABEL = "mongodb.com/search-namespace"
SEARCH_UID_LABEL = "mongodb.com/search-uid"
CLUSTER_NAME_LABEL = "mongodb.com/cluster-name"
COMPONENT_LABEL = "component"
MONGOT_COMPONENT = "mongot"
PROXY_COMPONENT = "search-proxy"
GA_STATE_OWNER_LABEL = "mongodb.com/v1.mongodbSearchResourceOwner"


def _operator_search_version(operator: Operator) -> str:
    pods = operator.list_operator_pods()
    assert pods, "no operator pods found"
    env_var = next(
        (entry for entry in pods[0].spec.containers[0].env if entry.name == "MDB_SEARCH_VERSION"),
        None,
    )
    assert env_var is not None, "MDB_SEARCH_VERSION not found in operator pod env"
    return env_var.value


def _assert_mongot_version_matches_operator(namespace: str, operator: Operator, phase: str) -> None:
    expected = _operator_search_version(operator)
    images = []
    for shard_name in _shard_names():
        sts = client.AppsV1Api().read_namespaced_stateful_set(
            search_resource_names.shard_statefulset_name(MDBS_RESOURCE_NAME, shard_name),
            namespace,
        )
        mongot = next(container for container in sts.spec.template.spec.containers if container.name == "mongot")
        images.append(mongot.image)
    actual = {image.rsplit(":", 1)[-1] for image in images}
    logger.info(f"Mongot version check ({phase}): operator MDB_SEARCH_VERSION={expected}, mongot images={images}")
    assert actual == {expected}, f"mongot image tags {actual} != operator MDB_SEARCH_VERSION {expected}"


def _shard_names() -> list[str]:
    return [f"{MDB_RESOURCE_NAME}-{idx}" for idx in range(SHARD_COUNT)]


def _uid(resource: Any) -> str:
    uid = resource.metadata.uid
    assert uid, f"{resource.metadata.name} has no UID"
    return str(uid)


def _managed_search_resources(namespace: str) -> dict[str, tuple[Any, str | None]]:
    apps = client.AppsV1Api()
    core = client.CoreV1Api()
    resources: dict[str, tuple[Any, str | None]] = {}

    for shard_name in _shard_names():
        resources[f"mongot-sts/{shard_name}"] = (
            apps.read_namespaced_stateful_set(
                search_resource_names.shard_statefulset_name(MDBS_RESOURCE_NAME, shard_name), namespace
            ),
            MONGOT_COMPONENT,
        )
        resources[f"mongot-service/{shard_name}"] = (
            core.read_namespaced_service(
                search_resource_names.shard_service_name(MDBS_RESOURCE_NAME, shard_name), namespace
            ),
            MONGOT_COMPONENT,
        )
        resources[f"mongot-configmap/{shard_name}"] = (
            core.read_namespaced_config_map(
                search_resource_names.shard_configmap_name(MDBS_RESOURCE_NAME, shard_name), namespace
            ),
            MONGOT_COMPONENT,
        )
        resources[f"mongot-proxy-service/{shard_name}"] = (
            core.read_namespaced_service(
                search_resource_names.shard_proxy_service_name(MDBS_RESOURCE_NAME, shard_name), namespace
            ),
            PROXY_COMPONENT,
        )
        resources[f"mongot-tls-secret/{shard_name}"] = (
            core.read_namespaced_secret(
                search_resource_names.shard_operator_managed_tls_secret_name(MDBS_RESOURCE_NAME, shard_name),
                namespace,
            ),
            MONGOT_COMPONENT,
        )

    resources["cluster-proxy-service"] = (
        core.read_namespaced_service(search_resource_names.proxy_service_name(MDBS_RESOURCE_NAME), namespace),
        PROXY_COMPONENT,
    )
    resources["envoy-deployment"] = (
        apps.read_namespaced_deployment(search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME), namespace),
        PROXY_COMPONENT,
    )
    resources["envoy-configmap"] = (
        core.read_namespaced_config_map(search_resource_names.lb_configmap_name(MDBS_RESOURCE_NAME), namespace),
        PROXY_COMPONENT,
    )
    resources["search-state-configmap"] = (
        core.read_namespaced_config_map(
            search_resource_names.search_state_configmap_name(MDBS_RESOURCE_NAME), namespace
        ),
        None,
    )
    return resources


def _source_objects(namespace: str, mdb: MongoDB, mongot_user: MongoDBUser) -> dict[str, Any]:
    core = client.CoreV1Api()
    resources: dict[str, Any] = {
        "sync-user-password-secret": core.read_namespaced_secret(
            mongot_user["spec"]["passwordSecretKeyRef"]["name"], namespace
        ),
        "envoy-server-certificate-secret": core.read_namespaced_secret(
            search_resource_names.lb_server_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX), namespace
        ),
        "envoy-client-certificate-secret": core.read_namespaced_secret(
            search_resource_names.lb_client_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX), namespace
        ),
        "source-ca-configmap": core.read_namespaced_config_map(CA_CONFIGMAP_NAME, namespace),
        "ops-manager-project-configmap": core.read_namespaced_config_map(mdb.config_map_name, namespace),
    }
    for shard_name in _shard_names():
        resources[f"source-search-tls-secret/{shard_name}"] = core.read_namespaced_secret(
            search_resource_names.shard_tls_cert_name(MDBS_RESOURCE_NAME, shard_name, MDBS_TLS_CERT_PREFIX),
            namespace,
        )
    return resources


def _assert_ga_metadata(resources: dict[str, tuple[Any, str | None]], search_uid: str, namespace: str) -> None:
    for key, (resource, _) in resources.items():
        owner_references = resource.metadata.owner_references or []
        assert any(
            ref.kind == "MongoDBSearch" and ref.name == MDBS_RESOURCE_NAME and str(ref.uid) == search_uid
            for ref in owner_references
        ), f"{key} must retain its GA MongoDBSearch ownerReference before upgrade"

        labels = resource.metadata.labels or {}
        assert SEARCH_UID_LABEL not in labels, f"{key} unexpectedly has the post-GA UID label"
        assert CLUSTER_NAME_LABEL not in labels, f"{key} unexpectedly has a cluster label in single-cluster mode"
        if key == "search-state-configmap":
            assert labels[GA_STATE_OWNER_LABEL] == MDBS_RESOURCE_NAME
            assert SEARCH_NAME_LABEL not in labels
            assert SEARCH_NAMESPACE_LABEL not in labels
        elif key.startswith("mongot-tls-secret/"):
            assert SEARCH_NAME_LABEL not in labels
            assert SEARCH_NAMESPACE_LABEL not in labels
        else:
            assert labels[SEARCH_NAME_LABEL] == MDBS_RESOURCE_NAME
            assert labels[SEARCH_NAMESPACE_LABEL] == namespace
        if key == "search-state-configmap" or key.startswith(("mongot-sts/", "mongot-tls-secret/")):
            assert COMPONENT_LABEL not in labels, f"{key} unexpectedly has the post-GA component label"


def _assert_current_metadata(resources: dict[str, tuple[Any, str | None]], search_uid: str, namespace: str) -> None:
    for key, (resource, component) in resources.items():
        assert not resource.metadata.owner_references, f"{key} still has legacy ownerReferences"
        labels = resource.metadata.labels or {}
        assert labels[SEARCH_NAME_LABEL] == MDBS_RESOURCE_NAME
        assert labels[SEARCH_NAMESPACE_LABEL] == namespace
        assert labels[SEARCH_UID_LABEL] == search_uid
        assert CLUSTER_NAME_LABEL not in labels, f"{key} has a cluster label in legacy single-cluster topology"
        if component is None:
            assert COMPONENT_LABEL not in labels, f"{key} should not have a workload component label"
        else:
            assert labels[COMPONENT_LABEL] == component


@fixture(scope="module")
def ga_operator(
    namespace: str,
    managed_security_context: str,
    operator_installation_config: dict[str, str],
    central_cluster_name: str,
    central_cluster_client: client.ApiClient,
    member_cluster_clients: list[MultiClusterClient],
    member_cluster_names: list[str],
) -> Operator:
    return install_official_operator(
        namespace,
        managed_security_context,
        operator_installation_config,
        central_cluster_name,
        central_cluster_client,
        member_cluster_clients,
        member_cluster_names,
        custom_operator_version=GA_OPERATOR_VERSION,
        helm_chart_path=MCK_HELM_CHART,
        operator_name=OPERATOR_NAME,
        operator_image=OFFICIAL_OPERATOR_IMAGE_NAME,
    )


@fixture(scope="module")
def migration_snapshot() -> dict[str, Any]:
    return {}


@fixture(scope="module")
def issuer(namespace: str) -> str:
    return ensure_search_issuer(namespace)


@fixture(scope="module")
def sharded_ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    return create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)


@fixture(scope="module")
def helper(namespace: str) -> SearchDeploymentHelper:
    return SearchDeploymentHelper(
        namespace=namespace,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        shard_count=SHARD_COUNT,
        mongods_per_shard=MONGODS_PER_SHARD,
        mongos_count=MONGOS_COUNT,
        config_server_count=CONFIG_SERVER_COUNT,
        tls_cert_prefix=MDBS_TLS_CERT_PREFIX,
        ca_configmap_name=CA_CONFIGMAP_NAME,
    )


@fixture(scope="module")
def mdb(namespace: str, sharded_ca_configmap: str, helper: SearchDeploymentHelper) -> MongoDB:
    return helper.create_sharded_mdb(set_tls_ca=True)


@fixture(scope="module")
def mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-sharded-managed-lb.yaml"),
        namespace=namespace,
        name=MDBS_RESOURCE_NAME,
    )
    if try_load(resource):
        return resource
    resource["spec"]["source"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME
    resource["spec"]["clusters"][0]["replicas"] = 1
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
        get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True),
        tools_pod=mongodb_tools_pod.get_tools_pod(namespace),
    )


@mark.e2e_operator_upgrade_search
class TestDeployOnMongoDBKubernetes190:

    def test_install_pinned_ga_operator(self, namespace: str, ga_operator: Operator):
        ga_operator.wait_for_operator_ready()
        assert ga_operator.operator_version == GA_OPERATOR_VERSION

        deployment = ga_operator.read_deployment()
        image = deployment.spec.template.spec.containers[0].image
        assert (
            image.rsplit(":", 1)[-1] == GA_OPERATOR_VERSION
        ), f"expected the pinned MCK {GA_OPERATOR_VERSION} operator image, got {image}"
        logger.info(
            f"Migration baseline resolved to MCK chart/CRD/operator bundle "
            f"{ga_operator.operator_version}, image={image}"
        )
        log_deployments_info(namespace)

    @skip_if_cloud_manager
    def test_create_ops_manager(self, namespace: str):
        ops_manager = get_ops_manager(namespace)
        assert ops_manager is not None
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_install_tls_certificates(
        self,
        namespace: str,
        issuer: str,
        sharded_ca_configmap: str,
        helper: SearchDeploymentHelper,
    ):
        assert sharded_ca_configmap == CA_CONFIGMAP_NAME
        helper.install_sharded_tls_certificates()
        create_lb_certificates(
            namespace,
            issuer,
            SHARD_COUNT,
            MDB_RESOURCE_NAME,
            MDBS_RESOURCE_NAME,
            MDBS_TLS_CERT_PREFIX,
        )
        create_per_shard_search_tls_certs(
            namespace,
            issuer,
            MDBS_TLS_CERT_PREFIX,
            SHARD_COUNT,
            MDB_RESOURCE_NAME,
            MDBS_RESOURCE_NAME,
        )

    def test_create_database_resource(self, mdb: MongoDB):
        mdb.update()
        mdb.assert_reaches_phase(Phase.Running, timeout=900)

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
        mdbs.assert_reaches_phase(Phase.Running, timeout=600)
        mdbs.load()
        mdbs.assert_lb_status()

    def test_verify_mongot_workloads(self, namespace: str):
        for shard_name in _shard_names():
            stateful_set = client.AppsV1Api().read_namespaced_stateful_set(
                search_resource_names.shard_statefulset_name(MDBS_RESOURCE_NAME, shard_name),
                namespace,
            )
            assert stateful_set.status.ready_replicas == 1

    def test_verify_mongot_version(self, namespace: str, ga_operator: Operator):
        _assert_mongot_version_matches_operator(namespace, ga_operator, "pre-upgrade")

    def test_wait_for_database_resource_ready(self, mdb: MongoDB):
        mdb.assert_reaches_phase(Phase.Running, timeout=600)

    def test_restore_sample_database(self, sample_movies_helper: SampleMoviesSearchHelper):
        sample_movies_helper.restore_sample_database()

    def test_create_search_index(self, sample_movies_helper: SampleMoviesSearchHelper):
        sample_movies_helper.create_search_index()

    def test_search_query_before_upgrade(self, sample_movies_helper: SampleMoviesSearchHelper):
        sample_movies_helper.assert_search_query(retry_timeout=120)

    def test_capture_ga_identity(
        self,
        namespace: str,
        mdb: MongoDB,
        mdbs: MongoDBSearch,
        mongot_user: MongoDBUser,
        sample_movies_helper: SampleMoviesSearchHelper,
        migration_snapshot: dict[str, Any],
    ):
        mdbs.load()
        search_uid = str(mdbs["metadata"]["uid"])
        resources = _managed_search_resources(namespace)
        _assert_ga_metadata(resources, search_uid, namespace)

        state_configmap = resources["search-state-configmap"][0]
        state = json.loads(state_configmap.data["state"])
        assert set(state["routingReadyMongotGroups"]) == set(_shard_names())

        pvc_uids: dict[str, str] = {}
        core = client.CoreV1Api()
        for shard_name in _shard_names():
            sts_name = search_resource_names.shard_statefulset_name(MDBS_RESOURCE_NAME, shard_name)
            pvc_names = mongot_data_pvc_names(namespace, sts_name)
            assert pvc_names, f"no mongot data PVCs found for {sts_name}"
            for pvc_name in pvc_names:
                pvc_uids[pvc_name] = _uid(core.read_namespaced_persistent_volume_claim(pvc_name, namespace))

        source_objects = _source_objects(namespace, mdb, mongot_user)
        for key, resource in source_objects.items():
            labels = resource.metadata.labels or {}
            assert SEARCH_NAME_LABEL not in labels, f"{key} must not be Search-owned"
            assert SEARCH_NAMESPACE_LABEL not in labels, f"{key} must not be Search-owned"
            assert SEARCH_UID_LABEL not in labels, f"{key} must not be Search-owned"

        query_results = list(sample_movies_helper.execute_example_search_query())
        assert len(query_results) == 4
        index_names = sorted(
            index["name"]
            for index in sample_movies_helper.search_tester.get_search_indexes(
                sample_movies_helper.db_name, sample_movies_helper.col_name
            )
        )
        assert index_names

        migration_snapshot.update(
            {
                "search_uid": search_uid,
                "search_clusters": deepcopy(mdbs["spec"]["clusters"]),
                "managed_uids": {key: _uid(resource) for key, (resource, _) in resources.items()},
                "managed_labels": {
                    key: deepcopy(resource.metadata.labels or {}) for key, (resource, _) in resources.items()
                },
                "managed_owner_references": {
                    key: [(ref.kind, ref.name, str(ref.uid)) for ref in (resource.metadata.owner_references or [])]
                    for key, (resource, _) in resources.items()
                },
                "pvc_uids": pvc_uids,
                "state_data": deepcopy(state_configmap.data),
                "source_uids": {key: _uid(resource) for key, resource in source_objects.items()},
                "source_data": {key: deepcopy(resource.data) for key, resource in source_objects.items()},
                "query_titles": sorted(result["title"] for result in query_results),
                "search_index_names": index_names,
            }
        )


@mark.e2e_operator_upgrade_search
class TestOperatorUpgrade:

    def test_upgrade_operator(
        self,
        namespace: str,
        operator_installation_config: dict[str, str],
        migration_snapshot: dict[str, Any],
    ):
        assert migration_snapshot["search_uid"], "GA identity snapshot must be captured before upgrade"
        operator = get_default_operator(
            namespace, operator_installation_config=operator_installation_config, apply_crds_first=True
        )
        operator.wait_for_operator_ready()
        log_deployments_info(namespace)

    def test_search_reconciled_by_current_operator(
        self,
        namespace: str,
        mdbs: MongoDBSearch,
        migration_snapshot: dict[str, Any],
    ):
        mdbs.assert_reaches_phase(phase=Phase.Running, timeout=600)

        def metadata_normalized() -> tuple[bool, str]:
            try:
                _assert_current_metadata(
                    _managed_search_resources(namespace),
                    migration_snapshot["search_uid"],
                    namespace,
                )
                return True, "all managed Search metadata normalized"
            except (AssertionError, client.ApiException) as exc:
                return False, str(exc)

        run_periodically(
            metadata_normalized,
            timeout=300,
            sleep_time=5,
            msg="current operator to adopt all GA Search resources",
        )

    def test_verify_mongot_version_after_upgrade(self, namespace: str, operator_installation_config: dict[str, str]):
        operator = Operator(namespace=namespace, helm_args=operator_installation_config)
        _assert_mongot_version_matches_operator(namespace, operator, "post-upgrade")

    def test_database_running_after_upgrade(self, mdb: MongoDB):
        mdb.assert_reaches_phase(phase=Phase.Running, timeout=600)

    def test_resource_identity_and_state_preserved(
        self,
        namespace: str,
        mdb: MongoDB,
        mdbs: MongoDBSearch,
        mongot_user: MongoDBUser,
        migration_snapshot: dict[str, Any],
    ):
        mdbs.load()
        assert str(mdbs["metadata"]["uid"]) == migration_snapshot["search_uid"]
        assert mdbs["spec"]["clusters"] == migration_snapshot["search_clusters"]

        resources = _managed_search_resources(namespace)
        assert {key: _uid(resource) for key, (resource, _) in resources.items()} == migration_snapshot["managed_uids"]
        assert resources["search-state-configmap"][0].data == migration_snapshot["state_data"]

        core = client.CoreV1Api()
        current_pvc_uids = {
            pvc_name: _uid(core.read_namespaced_persistent_volume_claim(pvc_name, namespace))
            for pvc_name in migration_snapshot["pvc_uids"]
        }
        assert current_pvc_uids == migration_snapshot["pvc_uids"]

        source_objects = _source_objects(namespace, mdb, mongot_user)
        assert {key: _uid(resource) for key, resource in source_objects.items()} == migration_snapshot["source_uids"]
        assert {key: resource.data for key, resource in source_objects.items()} == migration_snapshot["source_data"]

    def test_no_duplicate_resource_set(self, namespace: str):
        apps = client.AppsV1Api()
        core = client.CoreV1Api()
        selector = f"{SEARCH_NAME_LABEL}={MDBS_RESOURCE_NAME},{SEARCH_NAMESPACE_LABEL}={namespace}"
        shard_names = _shard_names()

        assert {
            item.metadata.name for item in apps.list_namespaced_stateful_set(namespace, label_selector=selector).items
        } == {
            search_resource_names.shard_statefulset_name(MDBS_RESOURCE_NAME, shard_name) for shard_name in shard_names
        }
        assert {
            item.metadata.name for item in apps.list_namespaced_deployment(namespace, label_selector=selector).items
        } == {search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME)}
        assert {
            item.metadata.name for item in core.list_namespaced_service(namespace, label_selector=selector).items
        } == {
            search_resource_names.proxy_service_name(MDBS_RESOURCE_NAME),
            *[
                name
                for shard_name in shard_names
                for name in (
                    search_resource_names.shard_service_name(MDBS_RESOURCE_NAME, shard_name),
                    search_resource_names.shard_proxy_service_name(MDBS_RESOURCE_NAME, shard_name),
                )
            ],
        }
        assert {
            item.metadata.name for item in core.list_namespaced_config_map(namespace, label_selector=selector).items
        } == {
            search_resource_names.lb_configmap_name(MDBS_RESOURCE_NAME),
            search_resource_names.search_state_configmap_name(MDBS_RESOURCE_NAME),
            *[search_resource_names.shard_configmap_name(MDBS_RESOURCE_NAME, shard_name) for shard_name in shard_names],
        }
        assert {
            item.metadata.name for item in core.list_namespaced_secret(namespace, label_selector=selector).items
        } == {
            search_resource_names.shard_operator_managed_tls_secret_name(MDBS_RESOURCE_NAME, shard_name)
            for shard_name in shard_names
        }

    def test_migration_metadata_normalized(
        self,
        namespace: str,
        mdb: MongoDB,
        mongot_user: MongoDBUser,
        migration_snapshot: dict[str, Any],
    ):
        resources = _managed_search_resources(namespace)
        _assert_current_metadata(resources, migration_snapshot["search_uid"], namespace)

        assert COMPONENT_LABEL not in migration_snapshot["managed_labels"]["mongot-sts/mdb-upgrade-sh-0"]
        assert not migration_snapshot["managed_labels"]["mongot-tls-secret/mdb-upgrade-sh-0"]
        assert migration_snapshot["managed_owner_references"]["search-state-configmap"]

        for key, resource in _source_objects(namespace, mdb, mongot_user).items():
            assert _uid(resource) == migration_snapshot["source_uids"][key]
            labels = resource.metadata.labels or {}
            assert SEARCH_NAME_LABEL not in labels, f"{key} was incorrectly adopted by Search"
            assert SEARCH_NAMESPACE_LABEL not in labels, f"{key} was incorrectly adopted by Search"
            assert SEARCH_UID_LABEL not in labels, f"{key} was incorrectly adopted by Search"

    def test_search_query_and_index_preserved(
        self,
        sample_movies_helper: SampleMoviesSearchHelper,
        migration_snapshot: dict[str, Any],
    ):
        sample_movies_helper.assert_search_query(retry_timeout=120)
        query_results = list(sample_movies_helper.execute_example_search_query())
        assert sorted(result["title"] for result in query_results) == migration_snapshot["query_titles"]
        assert (
            sorted(
                index["name"]
                for index in sample_movies_helper.search_tester.get_search_indexes(
                    sample_movies_helper.db_name, sample_movies_helper.col_name
                )
            )
            == migration_snapshot["search_index_names"]
        )
