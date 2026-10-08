from pytest import fixture
from tests.common.mongodb_tools_pod import mongodb_tools_pod


@fixture(scope="module")
def tools_pod(namespace: str, custom_mdb_version: str) -> mongodb_tools_pod.ToolsPod:
    return mongodb_tools_pod.get_tools_pod(namespace, custom_mdb_version)
