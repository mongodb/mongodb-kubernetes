from kubernetes import client
from kubetester import try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.constants import OPERATOR_NAME

STATIC_GA_ARCHITECTURE = "static-ga"
NON_STATIC_ARCHITECTURE = "non-static"
REPLICA_SET_NAME = "mdb"
AGENT_CONTAINER_NAME = "mongodb-agent"
AGENT_IMAGE_ENV = "AGENT_IMAGE"

# Dev OM build that supports desiredInfraRevision; replace with a released version once OM ships the field.
DEV_OM_VERSION = "10.0.0-alpha0.489.20261106T1228Z"


@fixture(scope="module")
def ops_manager(namespace: str, custom_appdb_version: str) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace
    )
    try_load(resource)
    resource.set_version(DEV_OM_VERSION)
    resource.set_appdb_version(custom_appdb_version)
    # Non-static AppDB uses the operator's AGENT_IMAGE instead of an OM version -> agent version mapping.
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = NON_STATIC_ARCHITECTURE
    return resource


@fixture(scope="module")
def agent_image(namespace: str) -> str:
    deployment = client.AppsV1Api().read_namespaced_deployment(OPERATOR_NAME, namespace)
    image = next(
        (env.value for env in deployment.spec.template.spec.containers[0].env or [] if env.name == AGENT_IMAGE_ENV),
        None,
    )
    assert image, f"{OPERATOR_NAME} deployment in namespace {namespace} has no {AGENT_IMAGE_ENV} env var"
    return image


@fixture(scope="module")
def replica_set(ops_manager: MongoDBOpsManager, namespace: str, custom_mdb_version: str, agent_image: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=REPLICA_SET_NAME,
    ).configure(ops_manager, REPLICA_SET_NAME)
    resource.set_version(custom_mdb_version)

    try_load(resource)
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = STATIC_GA_ARCHITECTURE
    # The dev OM reports an agent version with no published image, so the operator must use the one it was built with.
    resource["spec"].setdefault("podSpec", {}).setdefault("podTemplate", {}).setdefault("spec", {})["containers"] = [
        {"name": AGENT_CONTAINER_NAME, "image": agent_image}
    ]
    return resource


@mark.e2e_om_static_ga_infra_revision
def test_create_om(ops_manager: MongoDBOpsManager):
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_om_static_ga_infra_revision
def test_replica_set_reaches_running_phase(replica_set: MongoDB):
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=600, ignore_errors=True)


@mark.e2e_om_static_ga_infra_revision
def test_infra_revision_matches_desired_infra_revision(replica_set: MongoDB, namespace: str):
    update_revision = replica_set.read_statefulset().status.update_revision
    assert update_revision, f"StatefulSet {REPLICA_SET_NAME} has no status.updateRevision"

    processes = replica_set.get_automation_config_tester().automation_config["processes"]

    for idx in range(replica_set.get_members()):
        pod_name = f"{REPLICA_SET_NAME}-{idx}"

        infra_revision = KubernetesTester.run_command_in_pod_container(
            pod_name, namespace, ["printenv", "INFRA_REVISION"], container=AGENT_CONTAINER_NAME
        ).strip()
        assert infra_revision, f"INFRA_REVISION is not set on the mongodb-agent container of pod {pod_name}"

        matching = [p for p in processes if p["name"] in (pod_name, f"k8s/{namespace}/{pod_name}")]
        assert len(matching) == 1, (
            f"expected exactly one automation config process for pod {pod_name}, "
            f"got {[p['name'] for p in matching]}"
        )
        desired_infra_revision = matching[0].get("desiredInfraRevision")
        assert desired_infra_revision, f"automation config process for pod {pod_name} has no desiredInfraRevision"

        assert infra_revision == update_revision, (
            f"pod {pod_name} INFRA_REVISION {infra_revision!r} does not match "
            f"StatefulSet status.updateRevision {update_revision!r}"
        )
        assert desired_infra_revision == update_revision, (
            f"automation config process {matching[0]['name']!r} desiredInfraRevision {desired_infra_revision!r} "
            f"does not match StatefulSet status.updateRevision {update_revision!r}"
        )
