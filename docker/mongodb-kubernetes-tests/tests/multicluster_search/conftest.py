"""Package-local conftest for Q2-MC MongoDBSearch e2e tests."""

from pytest import fixture
from tests.common.mongodb_tools_pod import mongodb_tools_pod


@fixture(scope="module")
def tools_pod(namespace: str) -> mongodb_tools_pod.ToolsPod:
    """Tools pod used to run mongorestore against the source MongoDB deployment.

    Mirrors tests/search/conftest.py:tools_pod so MC scaffolds don't need to import
    that conftest. Pod is scheduled in the central cluster (its image-pull secret is
    seeded there by the multi-cluster namespace setup).
    """
    return mongodb_tools_pod.get_tools_pod(namespace)
