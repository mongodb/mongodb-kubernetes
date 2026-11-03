from typing import Optional

import pytest
from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import get_default_appdb_image_type, run_periodically
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture

OM_RESOURCE_NAME = "om-appdb-container-image-type"
IMAGE_TYPE_ANNOTATION = "mongodb.com/v1.containerImageType"

DEFAULT_IMAGE_TYPE = get_default_appdb_image_type()
# Override with whichever image type is not the operator default, so the annotation always changes the image.
ANNOTATED_IMAGE_TYPE = "ubi9" if DEFAULT_IMAGE_TYPE == "ubi9-slim" else "ubi9-slim"


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_basic.yaml"),
        name=OM_RESOURCE_NAME,
        namespace=namespace,
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    try_load(resource)
    return resource


def appdb_rolled_out_with_image_type(ops_manager: MongoDBOpsManager, appdb_version: str, image_type: str) -> bool:
    statefulset = ops_manager.read_appdb_statefulset()
    mongodb_container = next(
        container
        for container in statefulset.spec.template.spec.containers
        if "mongodb-enterprise-server" in container.image
    )
    if not mongodb_container.image.endswith(f":{appdb_version.removesuffix('-ent')}-{image_type}"):
        return False

    replicas = statefulset.spec.replicas
    sts_status = statefulset.status
    return (
        sts_status.observed_generation == statefulset.metadata.generation
        and sts_status.updated_replicas == replicas
        and sts_status.ready_replicas == replicas
    )


def assert_appdb_running_with_image_type(ops_manager: MongoDBOpsManager, appdb_version: str, image_type: str):
    # An annotation-only change does not bump metadata.generation, so assert_reaches_phase alone would return
    # immediately; wait for the statefulset to roll out the expected image first.
    assert run_periodically(
        lambda: appdb_rolled_out_with_image_type(ops_manager, appdb_version, image_type), timeout=900
    ), f"AppDB statefulset did not roll out the {image_type} image"
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)

    tester = ops_manager.get_appdb_tester(authenticate=True)
    tester.assert_connectivity()
    tester.assert_version(appdb_version)


@pytest.mark.e2e_om_appdb_container_image_type
class TestOpsManagerAppDBDefaultContainerImageType:
    def test_create_om(self, ops_manager: MongoDBOpsManager):
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)

    def test_appdb_uses_default_container_image_type(self, ops_manager: MongoDBOpsManager, custom_appdb_version: str):
        assert_appdb_running_with_image_type(ops_manager, custom_appdb_version, DEFAULT_IMAGE_TYPE)


@pytest.mark.e2e_om_appdb_container_image_type
class TestOpsManagerAppDBAnnotatedContainerImageType:
    def test_annotate_om(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["metadata"].setdefault("annotations", {})[IMAGE_TYPE_ANNOTATION] = ANNOTATED_IMAGE_TYPE
        ops_manager.update()

    def test_appdb_uses_annotated_container_image_type(self, ops_manager: MongoDBOpsManager, custom_appdb_version: str):
        assert_appdb_running_with_image_type(ops_manager, custom_appdb_version, ANNOTATED_IMAGE_TYPE)

    def test_om_is_running(self, ops_manager: MongoDBOpsManager):
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
