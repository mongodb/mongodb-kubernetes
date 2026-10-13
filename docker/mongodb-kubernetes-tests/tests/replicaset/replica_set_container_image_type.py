import pytest
from kubetester import try_load
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import get_default_image_type
from kubetester.mongodb import MongoDB
from kubetester.phase import Phase
from pytest import fixture

MDB_RESOURCE_NAME = "replica-set-container-image-type"
MDB_VERSION = "8.0.23"
IMAGE_TYPE_ANNOTATION = "mongodb.com/v1.containerImageType"

DEFAULT_IMAGE_TYPE = get_default_image_type()
# Override with whichever image type is not the operator default, so the annotation always changes the image.
ANNOTATED_IMAGE_TYPE = "ubi9" if DEFAULT_IMAGE_TYPE == "ubi9-slim" else "ubi9-slim"


@fixture(scope="module")
def replica_set(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        load_fixture("replica-set.yaml"),
        namespace=namespace,
        name=MDB_RESOURCE_NAME,
    )
    resource.set_version(MDB_VERSION)

    try_load(resource)
    return resource


def assert_mongodb_image_type(replica_set: MongoDB, image_type: str):
    statefulset = replica_set.read_statefulset()
    mongodb_container = next(
        container
        for container in statefulset.spec.template.spec.containers
        if "mongodb-enterprise-server" in container.image
    )
    assert mongodb_container.image.endswith(f":{MDB_VERSION}-{image_type}")


@pytest.mark.e2e_replica_set_container_image_type
def test_replica_set_uses_default_container_image_type(replica_set: MongoDB):
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running)

    assert_mongodb_image_type(replica_set, DEFAULT_IMAGE_TYPE)


@pytest.mark.e2e_replica_set_container_image_type
def test_replica_set_uses_annotated_container_image_type(replica_set: MongoDB):
    replica_set.load()
    replica_set["metadata"].setdefault("annotations", {})[IMAGE_TYPE_ANNOTATION] = ANNOTATED_IMAGE_TYPE
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running)

    assert_mongodb_image_type(replica_set, ANNOTATED_IMAGE_TYPE)
