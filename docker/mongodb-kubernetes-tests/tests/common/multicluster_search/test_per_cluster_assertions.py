"""Unit tests for per-cluster assertion helpers (mocked clients)."""

from unittest.mock import MagicMock

import pytest
from kubernetes.client.exceptions import ApiException
from tests.common.multicluster_search.per_cluster_assertions import (
    assert_deployment_ready_in_cluster,
    assert_resource_in_cluster,
)


def _ready_deployment(name: str) -> MagicMock:
    dep = MagicMock()
    dep.metadata.name = name
    dep.status.ready_replicas = 2
    dep.spec.replicas = 2
    return dep


def _not_ready_deployment(name: str) -> MagicMock:
    dep = MagicMock()
    dep.metadata.name = name
    dep.status.ready_replicas = 0
    dep.spec.replicas = 2
    return dep


def test_assert_deployment_ready_passes_when_replicas_match():
    apps = MagicMock()
    apps.read_namespaced_deployment.return_value = _ready_deployment("d")
    assert_deployment_ready_in_cluster(apps, name="d", namespace="ns")


def test_assert_deployment_ready_fails_when_replicas_short():
    apps = MagicMock()
    apps.read_namespaced_deployment.return_value = _not_ready_deployment("d")
    with pytest.raises(AssertionError, match="ready_replicas=0/2"):
        assert_deployment_ready_in_cluster(apps, name="d", namespace="ns")


def test_assert_resource_present_passes_when_found():
    core = MagicMock()
    core.read_namespaced_service.return_value = MagicMock()
    assert_resource_in_cluster(core, kind="Service", name="proxy-svc", namespace="ns")


def test_assert_resource_present_fails_when_404():
    core = MagicMock()
    core.read_namespaced_service.side_effect = ApiException(status=404)
    with pytest.raises(AssertionError, match="Service.*proxy-svc.*not found"):
        assert_resource_in_cluster(core, kind="Service", name="proxy-svc", namespace="ns")
