"""Shared fixtures for the MC-Search e2e tests.

Mirrors `tests/search/conftest.py` for the tools_pod fixture so the
data-plane tests have a pod for `mongorestore` runs. Other shared
fixtures (namespace, central_cluster_client, member_cluster_clients,
member_cluster_names, multi_cluster_operator) come from
`tests/conftest.py` and `tests/multicluster/conftest.py`.
"""

from pytest import fixture
from tests.common.mongodb_tools_pod import mongodb_tools_pod


@fixture(scope="module")
def tools_pod(namespace: str) -> mongodb_tools_pod.ToolsPod:
    return mongodb_tools_pod.get_tools_pod(namespace)
