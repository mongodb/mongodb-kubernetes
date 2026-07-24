import json
import os
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from kubernetes import client
from kubernetes.client.exceptions import ApiException
from kubetester import get_service, get_statefulset, run_periodically, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.omtester import skip_if_cloud_manager
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search.connectivity import (
    SEARCH_OWNER_NAME_LABEL,
    SEARCH_OWNER_NAMESPACE_LABEL,
    mongot_data_pvc_names,
    wait_for_mongot_pvcs_deleted,
    wait_for_search_deleted,
    wait_for_search_owned_resources_deleted,
)
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.search_resource_names import (
    lb_deployment_name,
    mongot_statefulset_name,
    proxy_service_name,
)
from tests.common.search.search_tester import SearchTester
from tests.conftest import get_default_operator, install_official_operator, log_deployments_info
from tests.constants import MCK_HELM_CHART, OFFICIAL_OPERATOR_IMAGE_NAME, OPERATOR_NAME
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

GA_OPERATOR_VERSION = "1.9.1"
TARGET_OPERATOR_VERSION = "1.10.0"
GA_MONGOT_VERSION = "1.70.1"

MDB_RESOURCE_NAME = "mdb-rs"

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = f"{ADMIN_USER_NAME}-password"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = f"{MONGOT_USER_NAME}-password"

USER_NAME = "mdb-user"
USER_PASSWORD = f"{USER_NAME}-password"

MONGOT_STATEFULSET_NAME = mongot_statefulset_name(MDB_RESOURCE_NAME)


@dataclass
class UpgradeState:
    """Pre-upgrade snapshot compared against the post-upgrade cluster state."""

    search_uid: str = ""
    pre_mongot_version: str = ""
    artifact_uids: dict[str, str] = field(default_factory=dict)
    mongot_pod_uid: str = ""
    mongot_pvc_uid: str = ""
    customer_input_uids: dict[str, str] = field(default_factory=dict)


def get_current_operator_release_version() -> str:
    release_path = Path("release.json")
    if not release_path.exists():
        project_dir = os.environ.get("PROJECT_DIR")
        assert project_dir, "release.json not found and PROJECT_DIR is not set"
        release_path = Path(project_dir) / "release.json"
    with release_path.open() as release_file:
        return json.load(release_file)["mongodbOperator"]


def expected_current_operator_image(operator_installation_config: dict[str, str]) -> str:
    registry = operator_installation_config["registry.operator"].rstrip("/")
    version = operator_installation_config["operator.version"]
    build = operator_installation_config.get("operator.build", "")
    return f"{registry}/{OFFICIAL_OPERATOR_IMAGE_NAME}:{version}{build}"


def assert_operator_image(operator: Operator, expected_image: str) -> None:
    deployment = operator.read_deployment()
    containers = [
        container for container in deployment.spec.template.spec.containers if container.name == OPERATOR_NAME
    ]
    assert len(containers) == 1, f"expected one {OPERATOR_NAME} container, got {[c.name for c in containers]}"
    assert containers[0].image == expected_image, f"operator image {containers[0].image} != {expected_image}"


def get_operator_search_version(operator: Operator) -> str:
    """Read MDB_SEARCH_VERSION from the operator pod."""
    pods = operator.list_operator_pods()
    assert len(pods) == 1, f"expected one operator pod, got {[pod.metadata.name for pod in pods]}"
    containers = [container for container in pods[0].spec.containers if container.name == OPERATOR_NAME]
    assert len(containers) == 1, f"expected one {OPERATOR_NAME} container, got {[c.name for c in containers]}"
    env_var = next((e for e in containers[0].env if e.name == "MDB_SEARCH_VERSION"), None)
    assert env_var is not None, "MDB_SEARCH_VERSION not found in operator pod env"
    return env_var.value


def image_tag(image: str) -> str:
    image_name, separator, tag = image.rpartition(":")
    assert separator and image_name and tag, f"image {image!r} has no tag"
    return tag


def get_mongot_image_tag(namespace: str) -> str:
    sts = get_statefulset(namespace, MONGOT_STATEFULSET_NAME)
    mongot = next((container for container in sts.spec.template.spec.containers if container.name == "mongot"), None)
    assert mongot is not None, f"StatefulSet {MONGOT_STATEFULSET_NAME} has no mongot container"
    return image_tag(mongot.image)


def assert_mongot_version(namespace: str, operator: Operator, expected: str, phase: str) -> str:
    operator_version = get_operator_search_version(operator)
    actual = get_mongot_image_tag(namespace)
    logger.info(
        f"Mongot version check ({phase}): expected={expected}, "
        f"operator MDB_SEARCH_VERSION={operator_version}, mongot image tag={actual}"
    )
    assert operator_version == expected, f"operator MDB_SEARCH_VERSION {operator_version} != {expected}"
    assert actual == expected, f"mongot image tag {actual} != operator MDB_SEARCH_VERSION {expected}"
    return actual


def wait_for_mongot_rollout(namespace: str, expected_version: str, previous_pod_uid: str) -> None:
    core = client.CoreV1Api()
    pod_name = f"{MONGOT_STATEFULSET_NAME}-0"

    def rolled_out() -> tuple[bool, str]:
        statefulset_version = get_mongot_image_tag(namespace)
        try:
            pod = core.read_namespaced_pod(pod_name, namespace)
        except ApiException as exc:
            if exc.status == 404:
                return False, f"Pod {pod_name} is absent"
            raise

        mongot = next((container for container in pod.spec.containers if container.name == "mongot"), None)
        if mongot is None:
            return False, f"Pod {pod_name} has no mongot container"
        mongot_status = next((s for s in pod.status.container_statuses or [] if s.name == "mongot"), None)
        if mongot_status is None:
            return False, f"Pod {pod_name} has no mongot container status"

        pod_version = image_tag(mongot.image)
        ready = bool(mongot_status.ready)
        replaced = pod.metadata.uid != previous_pod_uid
        complete = statefulset_version == pod_version == expected_version and ready and replaced
        return complete, (
            f"StatefulSet version={statefulset_version}, pod version={pod_version}, "
            f"ready={ready}, replaced={replaced}"
        )

    run_periodically(rolled_out, timeout=600, sleep_time=5, msg=f"mongot rollout to {expected_version}")


def list_search_artifacts(namespace: str) -> dict[str, list[Any]]:
    selector = f"{SEARCH_OWNER_NAME_LABEL}={MDB_RESOURCE_NAME},{SEARCH_OWNER_NAMESPACE_LABEL}={namespace}"
    apps = client.AppsV1Api()
    core = client.CoreV1Api()
    name_prefix = f"{MDB_RESOURCE_NAME}-search"

    def search_named(resources: list[Any]) -> list[Any]:
        return [
            resource
            for resource in resources
            if resource.metadata.name == name_prefix or resource.metadata.name.startswith(f"{name_prefix}-")
        ]

    return {
        "StatefulSets": search_named(apps.list_namespaced_stateful_set(namespace).items),
        "Services": search_named(core.list_namespaced_service(namespace).items),
        "ConfigMaps": search_named(core.list_namespaced_config_map(namespace).items),
        "Deployments": search_named(apps.list_namespaced_deployment(namespace).items),
        "Secrets": core.list_namespaced_secret(namespace, label_selector=selector).items,
    }


def search_owned_artifact_uids(namespace: str) -> dict[str, str]:
    result = {
        f"{kind}/{resource.metadata.name}": resource.metadata.uid
        for kind, resources in list_search_artifacts(namespace).items()
        for resource in resources
    }
    assert all(result.values()), f"Search artifacts missing UIDs: {result}"
    return result


def read_mongot_pod_and_pvc(namespace: str) -> tuple[Any, Any]:
    core = client.CoreV1Api()
    pod = core.read_namespaced_pod(f"{MONGOT_STATEFULSET_NAME}-0", namespace)
    pvc_names = mongot_data_pvc_names(namespace, MONGOT_STATEFULSET_NAME)
    assert len(pvc_names) == 1, f"expected one mongot PVC, got {pvc_names}"
    return pod, core.read_namespaced_persistent_volume_claim(pvc_names[0], namespace)


def assert_search_owner_reference(resource: Any, search_uid: str, expect_controller: bool) -> None:
    """The GA 1.9.1 operator stamps a single non-controller MongoDBSearch owner
    reference on Search resources; the upgraded operator stamps a single controller
    reference (controller=true, blockOwnerDeletion) as the native-GC backstop on the
    local cluster."""
    owner_references = resource.metadata.owner_references or []
    assert len(owner_references) == 1, (
        f"{resource.kind} {resource.metadata.name} has ownerReferences={owner_references}, "
        "expected exactly one MongoDBSearch owner"
    )
    owner = owner_references[0]
    assert owner.kind == "MongoDBSearch"
    assert owner.name == MDB_RESOURCE_NAME
    assert owner.uid == search_uid
    if expect_controller:
        assert owner.controller is True
    else:
        assert owner.controller is not True


def customer_input_uids(
    namespace: str,
    mdb: MongoDB,
    users: tuple[MongoDBUser, MongoDBUser, MongoDBUser],
) -> dict[str, str]:
    uids: dict[str, str] = {}
    for resource in (mdb, *users):
        resource.load()
        uid = resource["metadata"]["uid"]
        assert uid, f"{resource.name} has no UID"
        uids[f"custom-resource/{resource.name}"] = uid

    core = client.CoreV1Api()
    manager_configs = tuple(name for name in ("opsManager", "cloudManager") if name in mdb["spec"])
    assert len(manager_configs) == 1, f"expected exactly one manager config, found {manager_configs}"
    project_configmap = core.read_namespaced_config_map(mdb.config_map_name, namespace)
    assert project_configmap.metadata.uid, f"ConfigMap {mdb.config_map_name} has no UID"
    uids[f"configmap/{mdb.config_map_name}"] = project_configmap.metadata.uid

    for secret_name in (
        f"{ADMIN_USER_NAME}-password",
        f"{USER_NAME}-password",
        f"{MDB_RESOURCE_NAME}-{MONGOT_USER_NAME}-password",
    ):
        secret = core.read_namespaced_secret(secret_name, namespace)
        assert secret.metadata.uid, f"Secret {secret_name} has no UID"
        uids[f"secret/{secret_name}"] = secret.metadata.uid
    return uids


# --- Module-scoped fixtures (persist across upgrade) ---


@fixture(scope="module")
def upgrade_state() -> UpgradeState:
    return UpgradeState()


@fixture(scope="module")
def ga_operator(
    namespace: str,
    managed_security_context: str,
    operator_installation_config: dict[str, str],
) -> Operator:
    return install_official_operator(
        namespace,
        managed_security_context,
        operator_installation_config,
        central_cluster_name=None,
        central_cluster_client=None,
        member_cluster_clients=None,
        member_cluster_names=None,
        custom_operator_version=GA_OPERATOR_VERSION,
        helm_chart_path=MCK_HELM_CHART,
        operator_name=OPERATOR_NAME,
        operator_image=OFFICIAL_OPERATOR_IMAGE_NAME,
    )


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
    return helper.mongot_user_resource(mdbs.name, MONGOT_USER_NAME)


@fixture(scope="module")
def sample_movies_helper(mdb: MongoDB, namespace: str) -> SampleMoviesSearchHelper:
    return SampleMoviesSearchHelper(
        SearchTester.for_replicaset(mdb, USER_NAME, USER_PASSWORD),
        tools_pod=mongodb_tools_pod.get_tools_pod(namespace),
    )


@mark.e2e_operator_upgrade_search
class TestDeployOnOfficialOperator:

    def test_install_ga_operator(self, namespace: str, ga_operator: Operator):
        ga_operator.wait_for_operator_ready()
        assert_operator_image(ga_operator, f"quay.io/mongodb/{OFFICIAL_OPERATOR_IMAGE_NAME}:{GA_OPERATOR_VERSION}")
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

    def test_verify_mongot_version(self, namespace: str, ga_operator: Operator, upgrade_state: UpgradeState):
        upgrade_state.pre_mongot_version = assert_mongot_version(
            namespace, ga_operator, GA_MONGOT_VERSION, "pre-upgrade"
        )

    def test_wait_for_database_resource_ready(self, mdb: MongoDB):
        mdb.assert_abandons_phase(Phase.Running, timeout=300)
        mdb.assert_reaches_phase(Phase.Running, timeout=300)

    def test_restore_sample_database(self, sample_movies_helper: SampleMoviesSearchHelper):
        sample_movies_helper.restore_sample_database()

    def test_create_search_index(self, sample_movies_helper: SampleMoviesSearchHelper):
        sample_movies_helper.create_search_index()

    def test_search_query_before_upgrade(self, sample_movies_helper: SampleMoviesSearchHelper):
        sample_movies_helper.assert_search_query(retry_timeout=60)

    def test_capture_single_cluster_upgrade_state(
        self,
        namespace: str,
        mdbs: MongoDBSearch,
        mdb: MongoDB,
        admin_user: MongoDBUser,
        user: MongoDBUser,
        mongot_user: MongoDBUser,
        upgrade_state: UpgradeState,
    ):
        mdbs.load()
        upgrade_state.search_uid = mdbs["metadata"]["uid"]
        sts = get_statefulset(namespace, MONGOT_STATEFULSET_NAME)
        assert_search_owner_reference(sts, upgrade_state.search_uid, expect_controller=False)
        # GA never stamps the component label; its appearance after the upgrade
        # (together with the controller owner ref) is the adoption marker.
        labels = sts.metadata.labels or {}
        assert "component" not in labels, f"GA-created StatefulSet unexpectedly carries a component label: {labels}"

        pod, pvc = read_mongot_pod_and_pvc(namespace)
        upgrade_state.artifact_uids = search_owned_artifact_uids(namespace)
        upgrade_state.mongot_pod_uid = pod.metadata.uid
        upgrade_state.mongot_pvc_uid = pvc.metadata.uid
        assert upgrade_state.mongot_pod_uid
        assert upgrade_state.mongot_pvc_uid
        upgrade_state.customer_input_uids = customer_input_uids(namespace, mdb, (admin_user, user, mongot_user))


@mark.e2e_operator_upgrade_search
class TestOperatorUpgrade:

    def test_upgrade_operator(self, namespace: str, operator_installation_config: dict[str, str]):
        operator = get_default_operator(
            namespace, operator_installation_config=operator_installation_config, apply_crds_first=True
        )
        operator.wait_for_operator_ready()
        assert get_current_operator_release_version() == TARGET_OPERATOR_VERSION
        target_image = expected_current_operator_image(operator_installation_config)
        assert target_image != f"quay.io/mongodb/{OFFICIAL_OPERATOR_IMAGE_NAME}:{GA_OPERATOR_VERSION}"
        assert_operator_image(operator, target_image)
        log_deployments_info(namespace)

    def test_verify_mongot_version_after_upgrade(
        self,
        namespace: str,
        operator_installation_config: dict[str, str],
        upgrade_state: UpgradeState,
    ):
        operator = Operator(namespace=namespace, helm_args=operator_installation_config)
        expected = operator_installation_config["search.version"]
        assert expected != upgrade_state.pre_mongot_version, f"upgrade left MDB_SEARCH_VERSION unchanged at {expected}"
        operator_version = get_operator_search_version(operator)
        assert operator_version == expected, f"operator MDB_SEARCH_VERSION {operator_version} != {expected}"
        wait_for_mongot_rollout(namespace, expected, upgrade_state.mongot_pod_uid)
        assert get_mongot_image_tag(namespace) != upgrade_state.pre_mongot_version

    def test_search_running_after_upgrade(self, mdbs: MongoDBSearch):
        mdbs.assert_reaches_phase(phase=Phase.Running, timeout=300)

    def test_database_running_after_upgrade(self, mdb: MongoDB):
        mdb.assert_reaches_phase(phase=Phase.Running, timeout=600)

    def test_adopts_existing_single_cluster_artifacts(
        self,
        namespace: str,
        mdbs: MongoDBSearch,
        upgrade_state: UpgradeState,
    ):
        mdbs.load()
        assert mdbs["metadata"]["uid"] == upgrade_state.search_uid
        sts = get_statefulset(namespace, MONGOT_STATEFULSET_NAME)
        assert_search_owner_reference(sts, upgrade_state.search_uid, expect_controller=True)
        labels = sts.metadata.labels or {}
        assert (
            labels.get("component") == "mongot"
        ), f"upgraded operator must stamp component=mongot on the adopted StatefulSet: {labels}"

        pod, pvc = read_mongot_pod_and_pvc(namespace)
        assert search_owned_artifact_uids(namespace) == upgrade_state.artifact_uids
        assert pvc.metadata.uid == upgrade_state.mongot_pvc_uid
        assert pod.metadata.uid != upgrade_state.mongot_pod_uid, "mongot pod did not roll to the upgraded image"

    def test_search_query_after_upgrade(self, sample_movies_helper: SampleMoviesSearchHelper):
        sample_movies_helper.assert_search_query(retry_timeout=60)


@mark.e2e_operator_upgrade_search
class TestScaleWithManagedLB:

    def test_enable_multi_mongot_and_managed_lb(self, mdbs: MongoDBSearch):
        mdbs.load()
        mdbs["spec"]["clusters"] = [{"replicas": 2, "loadBalancer": {"managed": {}}}]
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


@mark.e2e_operator_upgrade_search
class TestPostUpgradeCleanup:

    def test_search_has_no_finalizers(self, mdbs: MongoDBSearch):
        mdbs.load()
        assert not (mdbs["metadata"].get("finalizers") or []), (
            f"MongoDBSearch has finalizers and cannot prove owner-reference GC with the operator down: "
            f"{mdbs['metadata'].get('finalizers')}"
        )

    def test_scale_operator_down(self, namespace: str):
        apps = client.AppsV1Api()
        core = client.CoreV1Api()
        apps.patch_namespaced_deployment_scale(OPERATOR_NAME, namespace, {"spec": {"replicas": 0}})

        def scaled_down() -> tuple[bool, str]:
            deployment = apps.read_namespaced_deployment(OPERATOR_NAME, namespace)
            spec_replicas = deployment.spec.replicas or 0
            current_replicas = deployment.status.replicas or 0
            ready_replicas = deployment.status.ready_replicas or 0
            operator_pods = core.list_namespaced_pod(
                namespace,
                label_selector=f"app.kubernetes.io/name={OPERATOR_NAME}",
            ).items
            done = spec_replicas == current_replicas == ready_replicas == 0 and not operator_pods
            pod_names = [pod.metadata.name for pod in operator_pods]
            return done, f"spec={spec_replicas}, current={current_replicas}, ready={ready_replicas}, pods={pod_names}"

        run_periodically(scaled_down, timeout=120, sleep_time=5, msg=f"Operator Deployment {OPERATOR_NAME} scale-down")

    def test_delete_search_resource(self, mdbs: MongoDBSearch):
        mdbs.delete()
        wait_for_search_deleted(mdbs, timeout=300)

    def test_owner_reference_gc_removes_search_artifacts(self, namespace: str):
        wait_for_search_owned_resources_deleted(
            client.AppsV1Api(),
            client.CoreV1Api(),
            namespace,
            MDB_RESOURCE_NAME,
            where=f"post-upgrade MongoDBSearch {namespace}/{MDB_RESOURCE_NAME}",
            timeout=300,
        )
        wait_for_mongot_pvcs_deleted(namespace, MONGOT_STATEFULSET_NAME, timeout=300)

    def test_customer_inputs_are_preserved(
        self,
        namespace: str,
        mdb: MongoDB,
        admin_user: MongoDBUser,
        user: MongoDBUser,
        mongot_user: MongoDBUser,
        upgrade_state: UpgradeState,
    ):
        assert customer_input_uids(namespace, mdb, (admin_user, user, mongot_user)) == (
            upgrade_state.customer_input_uids
        )
